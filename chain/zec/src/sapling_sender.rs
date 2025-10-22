use crate::error::ZecError;
use crate::network::Network;

use bip0039::{English, Mnemonic};
use rand_core::OsRng;

use zcash_keys::keys::UnifiedSpendingKey;
use zcash_keys::encoding::AddressCodec;

use sapling::{
    value::NoteValue, Rseed, Note, Node, MerklePath, IncrementalWitness, NOTE_COMMITMENT_TREE_DEPTH,
    Anchor,
    note_encryption::{/*SaplingDomain, */try_sapling_note_decryption, PreparedIncomingViewingKey, Zip212Enforcement},
    keys::Diversifier,
    //zip32::IncomingViewingKey
};
use zcash_primitives::{
    transaction::{
        builder::{BuildConfig, Builder as TxBuilder},
        fees::zip317,
        Transaction,
    },
    zip32::AccountId,
};
use zcash_proofs::prover::LocalTxProver;
use zcash_protocol::{
    consensus::{BlockHeight, BranchId as ZcashBranchId, Parameters},
    value::Zatoshis,
};
use zcash_transparent::{address::TransparentAddress, builder::TransparentSigningSet};

use std::io::{self, Read, Cursor};
use incrementalmerkletree::{frontier::CommitmentTree, Position, Hashable};
use zcash_encoding::{Optional, Vector};
use std::convert::Infallible;
use zcash_keys::keys::sapling::DiversifiableFullViewingKey;
use zcash_primitives::merkle_tree::write_incremental_witness;
use crate::keys::BranchId;

#[derive(Clone)]
pub struct SpendableNote {
    pub diversifier: Vec<u8>,
    pub value: u64,
    pub rseed: Vec<u8>,     // ZIP-212: raw 32B; pre-212: Fr bytes
    pub zip212: bool,
    pub witness: Vec<u8>,    // compact auth-path or legacy IncrementalWitness bytes
}

pub struct WitnessParts {
    pub witness_bytes: Vec<u8>,
    pub anchor_bytes: Vec<u8>, // enforce len == 32 in Rust
}

// #[derive(Clone)]
// pub struct SimSpendable {
//     pub note: SpendableNote,
//     pub anchor_bytes: Vec<u8>, // 32 bytes
// }

#[derive(Clone)]
pub struct DecryptedNote {
    pub diversifier: Vec<u8>,
    pub value: u64,
    pub rseed: Vec<u8>,
    pub zip212: bool,
}


/* ---------- Decoders ---------- */

fn read_node<R: Read>(r: &mut R) -> io::Result<Node> {
    let mut repr = [0u8; 32];
    r.read_exact(&mut repr)?;
    Option::from(Node::from_bytes(repr)).ok_or_else(|| {
        io::Error::new(
            io::ErrorKind::InvalidData,
            "Non-canonical encoding of Sapling node",
        )
    })
}

fn read_commitment_tree<R: Read>(
    r: &mut R,
) -> io::Result<CommitmentTree<Node, { NOTE_COMMITMENT_TREE_DEPTH }>> {
    // reborrow the same reader for each call
    let left  = Optional::read(&mut *r, read_node)?;
    let right = Optional::read(&mut *r, read_node)?;
    let parents = Vector::read(&mut *r, |rr| Optional::read(rr, read_node))?;

    CommitmentTree::from_parts(left, right, parents).map_err(|_| {
        io::Error::new(io::ErrorKind::InvalidData, "parents vector exceeded tree depth")
    })
}

fn read_incremental_witness_from_slice(bytes: &[u8]) -> io::Result<IncrementalWitness> {
    let mut rdr = Cursor::new(bytes);
    let tree   = read_commitment_tree(&mut rdr)?;
    let filled = Vector::read(&mut rdr, read_node)?;
    let cursor = Optional::read(&mut rdr, read_commitment_tree)?;

    IncrementalWitness::from_parts(tree, filled, cursor).ok_or_else(|| {
        io::Error::new(
            io::ErrorKind::InvalidData,
            "invalid witness: inconsistency detected between witness parts",
        )
    })
}

fn merkle_path_from_compact(bytes: &[u8]) -> io::Result<MerklePath> {
    // format: [DEPTH][ (len=32 || node 32b)*DEPTH reversed ][position u64 LE]
    if bytes.is_empty() {
        return Err(io::Error::new(io::ErrorKind::InvalidData, "empty witness"));
    }
    if bytes[0] != NOTE_COMMITMENT_TREE_DEPTH {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "depth is not as expected",
        ));
    }
    let rest = &bytes[1..];
    let needed = (NOTE_COMMITMENT_TREE_DEPTH as usize) * 33 + 8;
    if rest.len() < needed {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            "witness too short for auth path",
        ));
    }
    let (path_bytes, pos_bytes) = rest.split_at((NOTE_COMMITMENT_TREE_DEPTH as usize) * 33);

    let mut auth_path = Vec::with_capacity(NOTE_COMMITMENT_TREE_DEPTH as usize);
    for chunk in path_bytes.chunks_exact(33).rev() {
        if chunk[0] != 32 {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "auth-path element length != 32",
            ));
        }
        let mut slice = &chunk[1..];
        let node = read_node(&mut slice)?;
        auth_path.push(node);
    }

    let mut p = [0u8; 8];
    p.copy_from_slice(&pos_bytes[..8]);
    let position = Position::from(u64::from_le_bytes(p));

    MerklePath::from_parts(auth_path, position).map_err(|_| {
        io::Error::new(
            io::ErrorKind::InvalidData,
            "auth path had incorrect number of elements",
        )
    })
}

fn decode_merkle_path(bytes: &[u8]) -> Result<MerklePath, ZecError> {
    // try compact format first
    if let Ok(p) = merkle_path_from_compact(bytes) {
        return Ok(p);
    }
    // fall back to full IncrementalWitness serialization
    let iw = read_incremental_witness_from_slice(bytes)
        .map_err(|e| ZecError::GenericError(format!("decode witness: {e}")))?;
    iw.path()
        .ok_or_else(|| ZecError::GenericError("witness had no path".into()))
}

fn add_spends<U>(
    builder: &mut TxBuilder<'_, Network, U>,
    sapling_fvk: &DiversifiableFullViewingKey,
    notes: &[SpendableNote],
) -> Result<u64, ZecError>
where
    U: sapling::builder::ProverProgress,
{
    let mut total: u64 = 0;

    for s in notes {
        total = total
            .checked_add(s.value)
            .ok_or_else(|| ZecError::GenericError("value overflow".into()))?;

        // Vec<u8> -> [u8; 11]
        let diversifier_bytes: [u8; 11] = s
            .diversifier
            .as_slice()
            .try_into()
            .map_err(|_| ZecError::GenericError("Diversifier must be 11 bytes".into()))?;

        // Build Diversifier once
        let div = Diversifier(diversifier_bytes);

        // Address from DFVK + diversifier
        let addr = sapling_fvk
            .diversified_address(div)
            .ok_or_else(|| ZecError::GenericError("bad diversifier".into()))?;

        // Value
        let note_value = NoteValue::from_raw(s.value);

        // Vec<u8> -> [u8; 32] for rseed
        let rseed32: [u8; 32] = s
            .rseed
            .as_slice()
            .try_into()
            .map_err(|_| ZecError::GenericError("rseed must be 32 bytes".into()))?;

        let rseed = if s.zip212 {
            Rseed::AfterZip212(rseed32)
        } else {
            let fr = jubjub::Fr::from_bytes(&rseed32)
                .into_option()
                .ok_or_else(|| ZecError::GenericError("invalid pre-212 rseed (Fr)".into()))?;
            Rseed::BeforeZip212(fr)
        };

        // Reconstruct note from addr + value + rseed
        let note: Note = addr.create_note(note_value, rseed);

        // Witness → MerklePath
        let path = decode_merkle_path(&s.witness)?;

        // Register spend
        builder.add_sapling_spend::<Infallible>(sapling_fvk.fvk().clone(), note, path)?;
    }

    Ok(total)
}

/* ---------- MAIN: drain all selected shielded inputs to a transparent addr + OP_RETURN ---------- */

pub fn send_from_sapling_to_transparent(
    mnemonic: String,
    network: Network,
    notes_to_spend: Vec<SpendableNote>,
    to_transparent_address: String,
    anchor_bytes: Vec<u8>,
    target_height: u32,
    memo: Option<String>,                // NEW: transparent memo to embed in OP_RETURN
) -> Result<Vec<u8>, ZecError> {
        // decode recipient t-addr
    let to_taddr = TransparentAddress::decode(&network, &to_transparent_address)
        .map_err(|_| ZecError::InvalidAddress(to_transparent_address.clone()))?;

    // validate memo (if provided)
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
    let sapling_sk = usk.sapling();
    let sapling_fvk = sapling_sk.to_diversifiable_full_viewing_key();

    let anchor_arr: [u8; 32] = anchor_bytes
        .as_slice()
        .try_into()
        .map_err(|_| ZecError::GenericError("anchor must be 32 bytes".into()))?;

    let anchor = Anchor::from_bytes(anchor_arr)
        .into_option()
        .ok_or_else(|| ZecError::GenericError("bad Sapling anchor bytes".into()))?;

    let fee_rule = zip317::FeeRule::standard();
    
    // For non-mainnet networks (regtest/testnet), use a high enough block height
    // to ensure we get modern transaction versions (Overwinter or later)
    // This prevents "pre-overwinter transactions not supported" errors in test environments
    // The height is primarily used for consensus rule selection, not chain state validation
    // The actual anchor and chain state are validated separately
    let adjusted_height = match network {
        Network::Main => target_height,
        _ => std::cmp::max(target_height, 500000), // Force modern tx version for test networks
    };
    let height = BlockHeight::from_u32(adjusted_height);

    // ---------- PASS 1: mirror final structure to compute ZIP-317 fee ----------
    let mut b1 = TxBuilder::new(
        network,
        height,
        BuildConfig::Standard { sapling_anchor: Some(anchor), orchard_anchor: None },
    );

    let total_in = add_spends(&mut b1, &sapling_fvk, &notes_to_spend)?;

    // add the outputs that will exist in the real tx
    b1.add_transparent_output(
        &to_taddr,
        Zatoshis::from_u64(1).map_err(|_| ZecError::GenericError("bad amount".into()))?,
    )?;
    
    if let Some(ref data) = memo_bytes_opt {
        b1.add_transparent_null_data_output::<Infallible>(data)?;
    }

    let fee = b1.get_fee(&fee_rule)?;
    let amt = total_in.checked_sub(u64::from(fee))
        .ok_or_else(|| ZecError::GenericError("insufficient to cover fee".into()))?;
    if amt == 0 {
        return Err(ZecError::GenericError("nothing left to send after fee".into()));
    }

    // ---------- PASS 2: build the real transaction ----------
    let mut b2 = TxBuilder::new(
        network,
        height,
        BuildConfig::Standard { sapling_anchor: Some(anchor), orchard_anchor: None },
    );
    add_spends(&mut b2, &sapling_fvk, &notes_to_spend)?;

    let z = Zatoshis::from_u64(amt)
        .map_err(|_| ZecError::GenericError("amount out of range".into()))?;
    b2.add_transparent_output(&to_taddr, z)?;
    if let Some(data) = memo_bytes_opt {
        b2.add_transparent_null_data_output::<Infallible>(&data)?;
    }

    let prover = LocalTxProver::with_default_location()
        .ok_or_else(|| ZecError::GenericError("could not load proving keys".into()))?;
    let built = b2.build(
        &TransparentSigningSet::new(),
        &[sapling_sk.clone()],
        &[],
        OsRng,
        &prover,
        &prover,
        &fee_rule,
    )?;

    // serialize tx
    let tx: &Transaction = built.transaction();
    let mut out = Vec::new();
    tx.write(&mut out)
        .map_err(|e| ZecError::GenericError(format!("serialize tx: {e}")))?;
    Ok(out)
}

// Decrypts all notes in a single transaction that belong to the mnemonic.
pub fn decrypt_notes_in_tx(
    mnemonic: String,
    network: Network,
    tx_bytes: Vec<u8>,
    mined_height: u32,
) -> Result<Vec<DecryptedNote>, ZecError> {
    let mnemonic = Mnemonic::<English>::from_phrase(&mnemonic)?;
    let seed = mnemonic.to_seed("").to_vec();
    let usk = UnifiedSpendingKey::from_seed(&network, &seed, AccountId::ZERO)?;
    let sapling_sk = usk.sapling();
    let fvk = sapling_sk.to_diversifiable_full_viewing_key();
    let ivk = fvk.fvk().vk.ivk();

    // 2. Determine the Consensus Branch ID from the provided height
    let height = BlockHeight::from_u32(mined_height);
    let branch_id = ZcashBranchId::for_height(&network, height);
    let mut cur = Cursor::new(&tx_bytes[..]);
    let tx = Transaction::read(&mut cur, branch_id)
        .map_err(|e| ZecError::GenericError(format!("parse tx: {e}")))?;
    let sapling_bundle = tx.sapling_bundle().ok_or(ZecError::GenericError("Tx has no sapling bundle".into()))?;
    
    let zip212 = Zip212Enforcement::On;
    let pivk = PreparedIncomingViewingKey::new(&ivk);

    let mut found_notes = Vec::new();
    for output in sapling_bundle.shielded_outputs() {
        if let Some((note, addr, _memo)) = try_sapling_note_decryption(&pivk, output, zip212) {
            let (rseed_bytes, zip212_flag) = match note.rseed() {
                Rseed::AfterZip212(b) => (b.to_vec(), true),
                Rseed::BeforeZip212(fr) => (fr.to_bytes().to_vec(), false),
            };
            found_notes.push(DecryptedNote {
                diversifier: addr.diversifier().0.to_vec(),
                value: note.value().inner(),
                rseed: rseed_bytes,
                zip212: zip212_flag,
            });
        }
    }
    Ok(found_notes)
}

fn target_cmu_from_params(
    mnemonic: &str,
    network: Network,
    diversifier: [u8; 11],
    value: u64,
    rseed32: [u8; 32],
    zip212: bool,
) -> Result<[u8; 32], ZecError> {
    // derive DFVK (account 0)
    let m = Mnemonic::<English>::from_phrase(mnemonic)?;
    let seed = m.to_seed("").to_vec();
    let usk  = UnifiedSpendingKey::from_seed(&network, &seed, AccountId::ZERO)?;
    let dfvk = usk.sapling().to_diversifiable_full_viewing_key();

    // payment address from DFVK + diversifier
    let pa = dfvk.diversified_address(Diversifier(diversifier))
        .ok_or_else(|| ZecError::GenericError("bad diversifier".into()))?;

    // rseed
    let rseed = if zip212 {
        Rseed::AfterZip212(rseed32)
    } else {
        let fr = jubjub::Fr::from_bytes(&rseed32)
            .into_option()
            .ok_or_else(|| ZecError::GenericError("invalid pre-212 rseed (Fr)".into()))?;
        Rseed::BeforeZip212(fr)
    };

    // build note and compute cmu
    let note: Note = pa.create_note(NoteValue::from_raw(value), rseed);
    let node = Node::from_cmu(&note.cmu());
    Ok(node.to_bytes())
}

/// Build a real Sapling witness for `note` up to `anchor_height`
/// by scanning the chain’s Sapling outputs in block/tx order.
/// Returns witness bytes (zcash_primitives::merkle_tree::write_incremental_witness format)
/// and the 32-byte anchor root at `anchor_height`.
#[allow(clippy::too_many_arguments)]
pub fn build_witness_from_blocks(
    mnemonic: String,
    network: Network,
    diversifier: Vec<u8>,
    value: u64,
    rseed: Vec<u8>,
    zip212: bool,
    // raw tx bytes grouped by block, in ascending height order
    blocks_txs: Vec<Vec<Vec<u8>>>,
    branch_id_at_anchor: BranchId,
    expected_root: Option<Vec<u8>>,
) -> Result<WitnessParts, ZecError> {
    let branch_id = ZcashBranchId::from(branch_id_at_anchor);
    use incrementalmerkletree::frontier::CommitmentTree;
    use std::io::Cursor;
    use zcash_primitives::merkle_tree::write_incremental_witness;

    // Vec -> arrays
    let div_arr: [u8; 11] = diversifier.as_slice()
        .try_into()
        .map_err(|_| ZecError::GenericError("diversifier must be 11 bytes".into()))?;
    let rseed32: [u8; 32] = rseed.as_slice()
        .try_into()
        .map_err(|_| ZecError::GenericError("rseed must be 32 bytes".into()))?;

    // Precompute target cmu bytes
    let target_cmu_bytes =
        target_cmu_from_params(&mnemonic, network, div_arr, value, rseed32, zip212)?;

    let mut tree = CommitmentTree::<sapling::Node, { sapling::NOTE_COMMITMENT_TREE_DEPTH }>::empty();
    let mut witness: Option<sapling::IncrementalWitness> = None;
    let mut seen_target = false;

    for block in blocks_txs {
        for tx_bytes in block {
            let mut cur = Cursor::new(&tx_bytes[..]);
            let tx = Transaction::read(&mut cur, branch_id)
                .map_err(|e| ZecError::GenericError(format!("parse tx: {e}")))?;
            if let Some(bundle) = tx.sapling_bundle() {
                for out in bundle.shielded_outputs() {
                    let node = sapling::Node::from_cmu(out.cmu());

                    // append to tree
                    tree.append(node)
                        .map_err(|_| ZecError::GenericError("append failed".into()))?;

                    // match against *bytes* of the node
                    if !seen_target && node.to_bytes() == target_cmu_bytes {
                        witness = sapling::IncrementalWitness::from_tree(tree.clone());
                        if witness.is_none() {
                            return Err(ZecError::GenericError("failed to init witness".into()));
                        }
                        seen_target = true;
                    } else if seen_target {
                        witness.as_mut().unwrap().append(node)
                            .map_err(|_| ZecError::GenericError("witness append failed".into()))?;
                    }
                }
            }
        }
    }

    if !seen_target {
        return Err(ZecError::GenericError("target note cmu not found on chain".into()));
    }

    let anchor = tree.root().to_bytes(); // 32-byte anchor at the chosen height

    if let Some(expect) = expected_root {
        if expect.len() != 32 {
            return Err(ZecError::GenericError(format!(
                "expected_root must be 32 bytes, got {}",
                expect.len()
            )));
        }

        if expect.as_slice() != anchor {
            // check both endian orders
            // flip a cloned copy and try again
            let mut flipped = expect.clone();
            flipped.reverse();

            if flipped.as_slice() != anchor {
                return Err(ZecError::GenericError(format!(
                    "finalsaplingroot mismatch: expected {} or {}, got {}",
                    hex::encode(expect),
                    hex::encode(flipped),
                    hex::encode(anchor),
                )));
            }
        }
    }

    let mut wit_bytes = Vec::new();
    write_incremental_witness(&witness.unwrap(), &mut wit_bytes)
        .map_err(|e| ZecError::GenericError(format!("witness write: {e}")))?;
    let witness_parts = WitnessParts{
        witness_bytes: wit_bytes, 
        anchor_bytes: anchor.to_vec(),
    };
    Ok(witness_parts)
}

#[cfg(test)]
mod tests {
    use super::*;
    use zcash_transparent::address::TransparentAddress;
    use zcash_keys::encoding::AddressCodec; // for .encode()

    fn mk_valid_taddr(net: Network) -> String {
        // Deterministic, syntactically valid t-addr (pkh = 20 zero bytes).
        TransparentAddress::PublicKeyHash([0u8; 20]).encode(&net)
    }

    #[test]
    fn rejects_long_opreturn() {
        let net = Network::Test;
        let to_addr = mk_valid_taddr(net);
        let anchor = vec![0u8; 32]; // ok length; memo should fail first
        let notes: Vec<SpendableNote> = vec![];

        let res = send_from_sapling_to_transparent(
            "any mnemonic text; not parsed if memo too long".to_string(),
            net,
            notes,
            to_addr,
            anchor,
            1,
            Some("X".repeat(81)), // >80 bytes
        );

        // expect OP_RETURN length error
        match res {
            Err(ZecError::GenericError(msg)) => assert!(msg.contains("OP_RETURN")),
            other => panic!("expected OP_RETURN length error, got {other:?}"),
        }
    }

    #[test]
    fn rejects_invalid_address() {
        let net = Network::Test;
        let to_addr = "not-an-address".to_string();
        let anchor = vec![0u8; 32];
        let notes: Vec<SpendableNote> = vec![];

        let res = send_from_sapling_to_transparent(
            "dummy".to_string(),
            net,
            notes,
            to_addr,
            anchor,
            1,
            None,
        );

        assert!(matches!(res, Err(ZecError::InvalidAddress(_))));
    }

    #[test]
    fn rejects_wrong_anchor_length() {
        let net = Network::Test;
        let to_addr = mk_valid_taddr(net);
        let anchor = vec![0u8; 31]; // wrong length
        let notes: Vec<SpendableNote> = vec![];

        // Use a valid 12-word mnemonic so we get past mnemonic parsing
        let mnemonic = "abandon abandon abandon abandon abandon \
                        abandon abandon abandon abandon abandon abandon about";

        let res = send_from_sapling_to_transparent(
            mnemonic.to_string(),
            net,
            notes,
            to_addr,
            anchor,
            1,
            None,
        );

        match res {
            Err(ZecError::GenericError(msg)) => {
                assert!(msg.to_lowercase().contains("anchor"))
            }
            other => panic!("expected anchor-length error, got {other:?}"),
        }
    }
}
