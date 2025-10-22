use crate::error::ZecError;
use crate::network::Network;

use bip0039::{English, Mnemonic};
use rand_core::OsRng;

use zcash_keys::encoding::AddressCodec;
use zcash_keys::keys::UnifiedSpendingKey;
use zcash_primitives::{
    transaction::{
        builder::{BuildConfig, Builder as TxBuilder},
        fees::zip317,
        Transaction,
    },
    zip32::AccountId,
};
use zcash_protocol::{
    consensus::{BlockHeight, BranchId as ZcashBranchId, Parameters},
    value::Zatoshis,
};
use zcash_transparent::{address::TransparentAddress, builder::TransparentSigningSet};
use zcash_proofs::prover::LocalTxProver;

use std::convert::Infallible;
use std::io::{self, Cursor, Read};

use incrementalmerkletree::{frontier::CommitmentTree, Hashable, Position, Level};
use zcash_encoding::{Optional, Vector};
use crate::keys::BranchId;
use hex;

// ===== Orchard bits (crate names may vary a little by version) =====
use orchard::{
    keys::{FullViewingKey as OrchardFVK, Scope, SpendingKey as OrchardSpendingKey, PreparedIncomingViewingKey},
    note::{Note as OrchardNote, RandomSeed as OrchardRseed, Rho, ExtractedNoteCommitment},
    tree::{MerkleHashOrchard, MerklePath as OrchardMerklePath, Anchor as OrchardAnchor},
    NOTE_COMMITMENT_TREE_DEPTH as ORCHARD_MERKLE_DEPTH,
    Address as OrchardAddress,
    note_encryption::OrchardDomain,
    value::NoteValue as OrchardNoteValue,
};
use zcash_note_encryption::try_note_decryption;

#[derive(Clone)]
pub struct OrchardSpendableNote {
    pub diversifier: Vec<u8>,  // 11 bytes
    pub value: u64,            // zatoshis
    pub rseed: Vec<u8>,        // 32 bytes (Orchard is ZIP-212-only)
    pub rho: Vec<u8>,          // 32 bytes
    pub witness: Vec<u8>,      // compact auth-path or legacy IncrementalWitness encoding
    pub cmx: Vec<u8>,          // 32 bytes - store the actual commitment to avoid reconstruction mismatch
    pub addr_bytes: Vec<u8>,   // 43 bytes - store the exact address bytes to avoid derivation mismatch
}

pub struct OrchardWitnessParts {
    pub witness_bytes: Vec<u8>,
    pub anchor_bytes: Vec<u8>, // 32 bytes
}

/* ---------- Decode Orchard witness (compact path or full witness) ---------- */

// Orchard nodes are 32-byte hashes; the compact format we used for Sapling works the same:
// [DEPTH][ (len=32 || node 32b)*DEPTH reversed ][position u64 LE]
fn orchard_merkle_path_from_compact(bytes: &[u8]) -> io::Result<OrchardMerklePath> {
    
    if bytes.is_empty() {
        return Err(io::Error::new(io::ErrorKind::InvalidData, "empty witness"));
    }
    
    if bytes[0] != ORCHARD_MERKLE_DEPTH as u8 {
        return Err(io::Error::new(io::ErrorKind::InvalidData, "depth mismatch"));
    }
    
    let rest = &bytes[1..];
    let needed = ORCHARD_MERKLE_DEPTH * 33 + 8;
    
    if rest.len() < needed {
        return Err(io::Error::new(io::ErrorKind::InvalidData, "witness too short"));
    }
    let (path_bytes, pos_bytes) = rest.split_at(ORCHARD_MERKLE_DEPTH * 33);

    // Read auth path nodes in order
    let mut auth_path = [MerkleHashOrchard::empty_root(Level::ZERO); ORCHARD_MERKLE_DEPTH];
    
    for (i, slot) in auth_path.iter_mut().enumerate().take(ORCHARD_MERKLE_DEPTH) {
        let chunk_start = i * 33;
        let chunk_end = chunk_start + 33;
        let chunk = &path_bytes[chunk_start..chunk_end];

        if chunk[0] != 32 {
            return Err(io::Error::new(io::ErrorKind::InvalidData, "auth node len != 32"));
        }

        let mut node_bytes = [0u8; 32];
        node_bytes.copy_from_slice(&chunk[1..]);

        *slot = MerkleHashOrchard::from_bytes(&node_bytes)
            .into_option()
            .ok_or_else(|| io::Error::new(io::ErrorKind::InvalidData, "invalid Orchard node"))?;
    }

    let mut p = [0u8; 8];
    p.copy_from_slice(&pos_bytes[..8]);
    let position = u64::from_le_bytes(p);
    let pos_u32 = u32::try_from(position)
        .map_err(|_| io::Error::new(io::ErrorKind::InvalidData, "witness position too large"))?;
    // OrchardMerklePath::from_parts expects a u32 position
    let merkle_path = OrchardMerklePath::from_parts(pos_u32, auth_path);
    
    Ok(merkle_path)
}

fn decode_orchard_path(bytes: &[u8]) -> Result<OrchardMerklePath, ZecError> {
    
    let result = orchard_merkle_path_from_compact(bytes)
        .map_err(|e| ZecError::GenericError(format!("decode orchard witness: {e}")));
    
    result
}

/* ---------- Add Orchard spends to the builder ---------- */

fn add_orchard_spends(
    builder: &mut TxBuilder<'_, Network, ()>, // progress type usually `()`
    fvk: &OrchardFVK,
    notes: &[OrchardSpendableNote],
) -> Result<u64, ZecError> {
    let mut total: u64 = 0;

    for s in notes.iter() {
        
        total = total
            .checked_add(s.value)
            .ok_or_else(|| ZecError::GenericError("value overflow".into()))?;

        // Use the exact address bytes stored during decryption to avoid derivation mismatch
        let addr_bytes: [u8; 43] = s.addr_bytes.as_slice().try_into()
            .map_err(|_| ZecError::GenericError("Orchard address must be 43 bytes".into()))?;
        let pa = OrchardAddress::from_raw_address_bytes(&addr_bytes)
            .into_option()
            .ok_or_else(|| ZecError::GenericError("Invalid Orchard address bytes".into()))?;

        // rseed (always ZIP-212 style 32-byte for Orchard)
        let rseed32: [u8; 32] = s
            .rseed
            .as_slice()
            .try_into()
            .map_err(|_| ZecError::GenericError("rseed must be 32 bytes".into()))?;
        
        // rho from the spendable note
        let rho_bytes: [u8; 32] = s
            .rho
            .as_slice()
            .try_into()
            .map_err(|_| ZecError::GenericError("rho must be 32 bytes".into()))?;
        let rho = Rho::from_bytes(&rho_bytes)
            .into_option()
            .ok_or_else(|| ZecError::GenericError("invalid rho bytes".into()))?;
        
        let rseed = OrchardRseed::from_bytes(rseed32, &rho)
            .into_option()
            .ok_or_else(|| ZecError::GenericError("invalid rseed bytes".into()))?;

        // Reconstruct note
        let note = OrchardNote::from_parts(
            pa, 
            OrchardNoteValue::from_raw(s.value),
            rho,
            rseed
        ).into_option()
            .ok_or_else(|| ZecError::GenericError("invalid Orchard note parts".into()))?;

        let note_commitment = ExtractedNoteCommitment::from(note.commitment());
        
        // Validate that our reconstructed commitment matches the stored one
        let expected_cmx: [u8; 32] = s.cmx.as_slice().try_into()
            .map_err(|_| ZecError::GenericError("cmx must be 32 bytes".into()))?;
        let actual_cmx = note_commitment.to_bytes();
        
        if actual_cmx != expected_cmx {
            return Err(ZecError::GenericError(format!(
                "Note commitment mismatch: reconstructed {} but expected {}. This indicates a note reconstruction bug.",
                hex::encode(actual_cmx), hex::encode(expected_cmx)
            )));
        }

        // Witness → MerklePath
        let path = decode_orchard_path(&s.witness)?;
        
        // Debug: verify the path anchor matches what we expect

        // Register spend on the builder
        // (Method name may be `add_orchard_spend` in your `TxBuilder` version)
        builder.add_orchard_spend::<Infallible>(fvk.clone(), note, path)
            .map_err(|e| ZecError::GenericError(format!("add orchard spend: {e:?}")))?;
    }

    Ok(total)
}

/* ---------- MAIN: drain Orchard inputs → transparent + optional OP_RETURN ---------- */

#[allow(clippy::borrow_deref_ref)]
pub fn send_from_orchard_to_transparent(
    mnemonic: String,
    network: Network,
    notes_to_spend: Vec<OrchardSpendableNote>,
    to_transparent_address: String,
    anchor_bytes: Vec<u8>,
    target_height: u32,
    memo: Option<String>,
) -> Result<Vec<u8>, ZecError> {
    // transparent recipient
    let to_taddr = TransparentAddress::decode(&network, &to_transparent_address)
        .map_err(|_| ZecError::InvalidAddress(to_transparent_address.clone()))?;

    // OP_RETURN length check (zcashd limits nulldata to 80 bytes)
    let memo_bytes_opt = memo.map(|m| m.into_bytes());
    if let Some(ref d) = memo_bytes_opt {
        if d.len() > 80 {
            return Err(ZecError::GenericError("OP_RETURN exceeds 80 bytes".into()));
        }
    }

    // derive keys (account 0)
    let mnemonic = Mnemonic::<English>::from_phrase(&mnemonic)?;
    let seed = mnemonic.to_seed("").to_vec();
    let usk = UnifiedSpendingKey::from_seed(&network, &seed, AccountId::ZERO)?;
    let orchard_sk = usk.orchard();
    let orchard_fvk: OrchardFVK = (&*orchard_sk).into();

    // Orchard anchor
    let anchor_arr: [u8; 32] = anchor_bytes
        .as_slice()
        .try_into()
        .map_err(|_| ZecError::GenericError("anchor must be 32 bytes".into()))?;
    
    let orchard_anchor: OrchardAnchor = Option::from(OrchardAnchor::from_bytes(anchor_arr))
        .ok_or_else(|| ZecError::GenericError("bad Orchard anchor bytes".into()))?;

    // choose height (force modern rules on regtest/testnet)
    let adjusted_height = match network {
        Network::Main => target_height,
        _ => std::cmp::max(target_height, 500_000),
    };
    let height = BlockHeight::from_u32(adjusted_height);

    let fee_rule = zip317::FeeRule::standard();

    // ---- PASS 1: fee preview ----
    
    let mut b1 = TxBuilder::new(
        network,
        height,
        BuildConfig::Standard {
            sapling_anchor: None,
            orchard_anchor: Some(orchard_anchor),
        },
    );

    let total_in = add_orchard_spends(&mut b1, &orchard_fvk, &notes_to_spend)?;

    // outputs we’ll actually include
    b1.add_transparent_output(
        &to_taddr,
        Zatoshis::from_u64(1).map_err(|_| ZecError::GenericError("bad amount".into()))?,
    )?;
    if let Some(ref data) = memo_bytes_opt {
        b1.add_transparent_null_data_output::<Infallible>(data)?;
    }

    let fee = b1.get_fee(&fee_rule)?;
    let amt = total_in
        .checked_sub(u64::from(fee))
        .ok_or_else(|| ZecError::GenericError("insufficient to cover fee".into()))?;
    if amt == 0 {
        return Err(ZecError::GenericError(
            "nothing left to send after fee".into(),
        ));
    }

    // ---- PASS 2: real build ----
    
    let mut b2 = TxBuilder::new(
        network,
        height,
        BuildConfig::Standard {
            sapling_anchor: None,
            orchard_anchor: Some(orchard_anchor),
        },
    );
    
    add_orchard_spends(&mut b2, &orchard_fvk, &notes_to_spend)?;

    let z = Zatoshis::from_u64(amt)
        .map_err(|_| ZecError::GenericError("amount out of range".into()))?;
    b2.add_transparent_output(&to_taddr, z)?;
    if let Some(data) = memo_bytes_opt {
        b2.add_transparent_null_data_output::<Infallible>(&data)?;
    }

    // LocalTxProver implements both Sapling (Groth16) & Orchard (Halo2) provers
    let prover = LocalTxProver::with_default_location()
        .ok_or_else(|| ZecError::GenericError("could not load proving keys".into()))?;

    
    let built = match b2.build(
        &TransparentSigningSet::new(),
        &[],                    // no Sapling keys
        &[(&*orchard_sk).into()],  // Orchard spending key(s)
        OsRng,
        &prover,                // sapling prover (unused here)
        &prover,                // orchard prover
        &fee_rule,
    ) {
        Ok(transaction) => {
            transaction
        }
        Err(e) => {
            return Err(ZecError::GenericError(format!("build transaction: {e:?}")));
        }
    };

    // serialize
    let tx: &Transaction = built.transaction();
    let mut out = Vec::new();
    tx.write(&mut out)
        .map_err(|e| ZecError::GenericError(format!("serialize tx: {e}")))?;
    Ok(out)
}

/* ---------- Decrypt Orchard notes that belong to our mnemonic in one tx ---------- */

pub struct OrchardDecryptedNote {
    pub diversifier: Vec<u8>,
    pub value: u64,
    pub rseed: Vec<u8>,
    pub rho: Vec<u8>,  // Store rho for proper note reconstruction
    pub cmx: Vec<u8>,  // Store the actual commitment
    pub addr_bytes: Vec<u8>,  // Store the exact address bytes to avoid derivation mismatch
}

#[allow(clippy::borrow_deref_ref)]
pub fn decrypt_orchard_notes_in_tx(
    mnemonic: String,
    network: Network,
    tx_bytes: Vec<u8>,
    mined_height: u32,
) -> Result<Vec<OrchardDecryptedNote>, ZecError> {
    let mnemonic = Mnemonic::<English>::from_phrase(&mnemonic)?;
    let seed = mnemonic.to_seed("").to_vec();
    let usk = UnifiedSpendingKey::from_seed(&network, &seed, AccountId::ZERO)?;
    let orchard_sk = usk.orchard();
    let ofvk: OrchardFVK = (&*orchard_sk).into();

    let height = BlockHeight::from_u32(mined_height);
    let branch_id = ZcashBranchId::for_height(&network, height);

    let mut cur = Cursor::new(&tx_bytes[..]);
    let tx = Transaction::read(&mut cur, branch_id)
        .map_err(|e| ZecError::GenericError(format!("parse tx: {e}")))?;

    let mut found = Vec::new();

    if let Some(bundle) = tx.orchard_bundle() {
        // Use external scope IVK for incoming funds
        let ivk = ofvk.to_ivk(Scope::External);
        let prepared_ivk = PreparedIncomingViewingKey::new(&ivk);

        for action in bundle.actions() {
            // Try note decryption for each action with our IVK.
            let domain = OrchardDomain::for_action(action);
            if let Some((note, addr, _memo)) = try_note_decryption(&domain, &prepared_ivk, action) {
                    // Extract data
                let div = addr.diversifier();
                let d = div.as_array();
                let rseed = note.rseed().as_bytes();
                let v = note.value().inner();
                let rho = note.rho().to_bytes();
                
                // Compute the commitment of this note to verify it's correct
                let note_cmx = ExtractedNoteCommitment::from(note.commitment());
                
                
                // Store the actual commitment for this note
                
                found.push(OrchardDecryptedNote {
                    diversifier: d.to_vec(),
                    value: v,
                    rseed: rseed.to_vec(),
                    rho: rho.to_vec(),
                    cmx: note_cmx.to_bytes().to_vec(),
                    addr_bytes: addr.to_raw_address_bytes().to_vec(),
                });
            }
        }
    }
    Ok(found)
}

/* ---------- Build Orchard witness from blocks (scan cmx in order) ---------- */

#[allow(clippy::too_many_arguments)]
pub fn build_orchard_witness_from_blocks(
    #[allow(unused_variables)]
    _mnemonic: String,
    _network: Network,
    diversifier: Vec<u8>,
    _value: u64,
    rseed: Vec<u8>,
    rho: Vec<u8>,  // Add rho parameter
    cmx: Vec<u8>,  // Add the actual commitment
    blocks_txs: Vec<Vec<Vec<u8>>>,
    branch_id_at_anchor: BranchId,
    expected_root: Option<Vec<u8>>,
) -> Result<OrchardWitnessParts, ZecError> {
    use incrementalmerkletree::frontier::CommitmentTree;
    use std::io::Cursor;
    use orchard::tree::MerkleHashOrchard;
    
    let branch_id = ZcashBranchId::from(branch_id_at_anchor);
    
    // Convert Vec to arrays
    let _div_arr: [u8; 11] = diversifier.as_slice()
        .try_into()
        .map_err(|_| ZecError::GenericError("diversifier must be 11 bytes".into()))?;
    let _rseed32: [u8; 32] = rseed.as_slice()
        .try_into()
        .map_err(|_| ZecError::GenericError("rseed must be 32 bytes".into()))?;
    let _rho32: [u8; 32] = rho.as_slice()
        .try_into()
        .map_err(|_| ZecError::GenericError("rho must be 32 bytes".into()))?;

    // Use the provided commitment directly instead of trying to reconstruct it
    let target_cmx_bytes: [u8; 32] = cmx.as_slice()
        .try_into()
        .map_err(|_| ZecError::GenericError("cmx must be 32 bytes".into()))?;

    // Initialize Orchard commitment tree
    let mut tree = CommitmentTree::<MerkleHashOrchard, { ORCHARD_MERKLE_DEPTH as u8 }>::empty();
    let mut witness: Option<incrementalmerkletree::witness::IncrementalWitness<MerkleHashOrchard, { ORCHARD_MERKLE_DEPTH as u8 }>> = None;
    let mut seen_target = false;

    
    // Process blocks
    for block in blocks_txs.iter() {
        
        for tx_bytes in block.iter() {
            let mut cur = Cursor::new(&tx_bytes[..]);
            let tx = Transaction::read(&mut cur, branch_id)
                .map_err(|e| ZecError::GenericError(format!("parse tx: {e}")))?;
            
            if let Some(bundle) = tx.orchard_bundle() {
                // Found Orchard bundle with actions
                
                for action in bundle.actions().iter() {
                    
                    // Get the commitment from the action
                    let cmx = *action.cmx();
                    let cmx_bytes = cmx.to_bytes();
                    let node = MerkleHashOrchard::from_cmx(&cmx);

                    
                    // Append to tree
                    tree.append(node)
                        .map_err(|_| ZecError::GenericError("append failed".into()))?;


                    // Check if this is our target note
                    if !seen_target && cmx_bytes == target_cmx_bytes {
                        
                        witness = incrementalmerkletree::witness::IncrementalWitness::from_tree(tree.clone());
                        if witness.is_none() {
                            return Err(ZecError::GenericError("failed to init witness".into()));
                        }
                        seen_target = true;
                    } else if seen_target {
                        witness.as_mut().unwrap().append(node)
                            .map_err(|_| ZecError::GenericError("witness append failed".into()))?;
                    } else {
                        // Pre-target, just note the cmx
                    }
                }
            }
        }
    }


    if !seen_target {
        return Err(ZecError::GenericError("target note cmx not found on chain".into()));
    }

    // Get the root/anchor
    let anchor = tree.root();
    let anchor_bytes = anchor.to_bytes();

    // Check expected root if provided
    if let Some(expect) = expected_root {
        if expect.len() != 32 {
            return Err(ZecError::GenericError(format!(
                "expected_root must be 32 bytes, got {}",
                expect.len()
            )));
        }

        if expect.as_slice() != anchor_bytes {
            // Check both endian orders
            let mut flipped = expect.clone();
            flipped.reverse();

            if flipped.as_slice() != anchor_bytes {
                return Err(ZecError::GenericError(format!(
                    "finalorchardroot mismatch: expected {} or {}, got {}",
                    hex::encode(expect),
                    hex::encode(flipped),
                    hex::encode(anchor_bytes),
                )));
            }
        }
    }

    // Serialize witness
    // For Orchard, we'll use a compact representation similar to Sapling
    let witness_unwrapped = witness.unwrap();
    let witness_path = witness_unwrapped.path().unwrap();
    
    
    
    let mut witness_bytes = Vec::new();
    
    // Write depth
    witness_bytes.push(ORCHARD_MERKLE_DEPTH as u8);
    
    // Write auth path nodes in order from the witness
    // The path_elems() returns the authentication path
    for node in witness_path.path_elems().iter().take(ORCHARD_MERKLE_DEPTH) {
        witness_bytes.push(32); // node length
        witness_bytes.extend_from_slice(&node.to_bytes());
    }
    
    // Write position
    let position = u64::from(witness_path.position());
    witness_bytes.extend_from_slice(&position.to_le_bytes());

    Ok(OrchardWitnessParts {
        witness_bytes,
        anchor_bytes: anchor_bytes.to_vec(),
    })
}
