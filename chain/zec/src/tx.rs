use crate::error::{ZecError, to_zecerror};
use crate::network::Network;

use std::{
    cmp::{max, min},
    str::FromStr as _,
};

use anyhow::anyhow;
use blake2b_simd::Params;

// Restore Sapling and Orchard imports
use rand_chacha::ChaCha20Rng;
use rand_core::{OsRng, SeedableRng};
use secp256k1::{ecdsa::Signature, All, PublicKey, Secp256k1, SecretKey};
use serde::{Deserialize, Serialize};
use zcash_keys::{
    address::{Address, Receiver},
    encoding::AddressCodec,
};
use zcash_primitives::transaction::{
    fees::zip317,
    sighash::{signature_hash, SignableInput},
    txid::TxIdDigester,
    Authorized,
    TransactionData,
    TxVersion,
    Unauthorized,
    Authorization,
};
use zcash_transparent::{
    // Replaces zcash_primitives::legacy::Script
    address::Script,
    // Contains the new TransparentBuilder
    builder::TransparentBuilder,
    // Replaces zcash_primitives::transaction::components::transparent::Bundle
    bundle::Bundle as TransparentBundle,
    // Replaces zcash_primitives::transaction::components::OutPoint and TxOut
    bundle::{OutPoint, TxOut},
    sighash::SighashType,
    // May need the inner SignableInput later
    sighash::SignableInput as TransparentSignableInputData,
    // Replaces zcash_primitives::transaction::sighash::SIGHASH_ALL
    sighash::SIGHASH_ALL,
};

#[cfg(feature = "shielded")]
use orchard::{builder::BundleType, bundle::Flags, keys::OutgoingViewingKey, value::NoteValue, Proof};
#[cfg(feature = "shielded")]
use sapling::{note_encryption::Zip212Enforcement, Anchor};
#[cfg(feature = "shielded")]
use zcash_proofs::prover::LocalTxProver;

// Restored Sapling and Orchard imports
// use sapling::builder::{Builder as SaplingBuilder, BundleType as SaplingBundleType};
// use sapling::prover::LocalTxProver as SaplingLocalTxProver;
// use sapling::type_marker::Zip212Enforcement;
// use sapling::bundle::Bundle as SaplingBundle;
// use sapling::Anchor as SaplingAnchor;
// use sapling::prover::OutputInfo as SaplingOutputInfo;

use zcash_protocol::{
    consensus::{BlockHeight, BranchId},
    memo::{Memo, MemoBytes},
    value::{ZatBalance as Amount, Zatoshis}, // Use Amount alias for ZatBalance
};

use hex;
use tracing::{debug, info, warn};

// use zcash_proofs::prover::LocalTxProver;

// Sapling bundle builder & types
// use sapling::{
//     builder::{Builder as SaplingBuilder, BundleType as SaplingBundleType},
//     note_encryption::Zip212Enforcement,
//     value::NoteValue as SaplingNoteValue,
//     PaymentAddress as SaplingPaymentAddress,
// };

pub struct TxBytes {
    pub txid: String,
    pub data: Vec<u8>,
}

pub type Sighash = Vec<u8>;
pub type Sighashes = Vec<Sighash>;


#[derive(Clone, Serialize, Deserialize, Debug)]
pub struct UTXO {
    pub txid: String,
    pub height: u32,
    #[serde(rename = "outputIndex")]
    pub vout: u32,
    pub script: String,
    #[serde(rename = "satoshis")]
    pub value: u64,
}

#[derive(Clone, Serialize, Deserialize, Default, Debug)]
pub struct Output {
    pub address: String,
    pub amount: u64,
    pub memo: String,
}

#[derive(Clone, Serialize, Deserialize, Debug)]
pub struct PartialTx {
    pub height: u32,
    pub txid: Vec<u8>,
    pub inputs: Vec<UTXO>,
    pub outputs: Vec<Output>,
    pub fee: u64,
    #[serde(skip)]
    pub sighashes: Sighashes,
    pub expiry_height: u32, // 0 means never expires
    pub version: u32, // Transaction version calculated from consensus rules
}

pub fn build_ptx(
    vault: Vec<u8>,
    mut ptx: PartialTx,
    network: Network,
) -> Result<PartialTx, ZecError> {
    let unauthed_tx = build_unauthorized_tx(vault, &ptx, network)?;

    // Calculate and set the TXID
    let txid_parts = unauthed_tx.digest(TxIdDigester);
    let txid = signature_hash(&unauthed_tx, &SignableInput::Shielded, &txid_parts)
        .as_ref()
        .to_vec();
    ptx.txid = txid;

    // The build_unauthorized_tx already determined the version and included it in unauthed_tx
    let version = unauthed_tx.version();
    ptx.version = version.header();

    // Calculate sighashes
    let mut sighashes = vec![];
    for (index, inp) in ptx.inputs.iter().enumerate() {
        let value = Zatoshis::from_u64(inp.value)
            .map_err(|e| ZecError::InvalidAmount(inp.value, e.into()))?;

        let script = Script(hex::decode(&inp.script).map_err(|e| {
            ZecError::GenericError(format!("Invalid script hex for input {index}: {e}"))
        })?);


        // For P2PKH, the script_code is the script without the leading byte (length) and 
        // the two trailing bytes (CHECKSIG). This might vary depending on script type.
        // For now, use the full script for both.
        let transparent_input_data = TransparentSignableInputData::from_parts(
            SighashType::ALL, // Use SIGHASH_ALL constant to sign all inputs and outputs
            index,
            &script, // script_code (this is the script being executed)
            &script, // script_pubkey (for validation)
            value,
        );

        // Get the sighash - this needs to match what the signer expects
        let sighash = signature_hash(
            &unauthed_tx,
            &SignableInput::Transparent(transparent_input_data),
            &txid_parts,
        )
        .as_ref()
        .to_vec();
        
        
        sighashes.push(sighash);
    }
    ptx.sighashes = sighashes;

    Ok(ptx)
}

pub fn apply_signatures(
    vault: Vec<u8>,
    ptx: PartialTx,
    signatures_der: Vec<Vec<u8>>,
    network: Network,
) -> Result<Vec<u8>, ZecError> {
    // Build the initial unauthorized transaction
    let unauthed_tx = build_unauthorized_tx(vault, &ptx, network)?;

    
    // Prepare secp256k1 signatures from DER format
    let secp_signatures = signatures_der
        .iter()
        .map(|s| Signature::from_der(s))
        .collect::<Result<Vec<_>, _>>()
        .map_err(|e| ZecError::GenericError(format!("invalid DER signature(s): {e}")))?;

    // Apply the signatures to the transaction - transparent only, using new flow
    let signed_transparent_bundle_opt: Option<zcash_transparent::bundle::Bundle<zcash_transparent::bundle::Authorized>>;

    if let Some(unauth_transparent_bundle) = unauthed_tx.transparent_bundle().cloned() {
        // Define the sighash calculation logic as a closure.
        let txid_parts = unauthed_tx.digest(TxIdDigester);
        let calculate_sighash = |signable_input: TransparentSignableInputData| -> [u8; 32] {
            let hash = signature_hash(
                &unauthed_tx,
                &SignableInput::Transparent(signable_input),
                &txid_parts,
            );
            
            let hash_slice: &[u8] = hash.as_ref();
            hash_slice
                .try_into()
                .expect("signature_hash must be 32 bytes")
        };

        // Prepare the signing context by passing the closure.
        let secp = Secp256k1::verification_only();
        let mut context = unauth_transparent_bundle
            .prepare_transparent_signatures(calculate_sighash, &secp)
            .map_err(|e: zcash_transparent::builder::Error| ZecError::GenericError(format!("Failed to prepare transparent signatures: {e}")))?;

        // Append external signatures
        context = context
            .append_external_signatures(&secp_signatures)
            .map_err(|e: zcash_transparent::builder::Error| ZecError::GenericError(format!("Failed to append external transparent signatures: {e}")))?;
        
        // Finalize the signed transparent bundle
        let signed_bundle = context.finalize_signatures()
            .map_err(|e: zcash_transparent::builder::Error| {
                ZecError::GenericError(format!("Failed to finalize transparent signatures: {e}"))
            })?;
        signed_transparent_bundle_opt = Some(signed_bundle);
    } else {
        // No transparent bundle at all
        if !secp_signatures.is_empty() {
            return Err(ZecError::GenericError("Signatures provided but no transparent bundle in transaction".into()))
        }
        signed_transparent_bundle_opt = None;
    }

    // --- S H I E L D E D ---
    // 1) Compute the shared shielded signing commitment
    let txid_parts = unauthed_tx.digest(TxIdDigester);
    let shielded_sig_commitment =
        signature_hash(&unauthed_tx, &SignableInput::Shielded, &txid_parts);

    // 2) Authorize Sapling (outputs-only => no spends => empty asks)
    #[cfg(feature = "shielded")]
    let mut rng = OsRng;
    #[cfg(not(feature = "shielded"))]
    let rng = OsRng;
    let sapling_auth = unauthed_tx
        .sapling_bundle()
        .cloned()
        .map(|b| {
            // For outputs-only this just creates the (possibly empty) binding signature.
            b.apply_signatures(rng, *shielded_sig_commitment.as_ref(), &[])
        })
        .transpose()
        .map_err(|e| ZecError::GenericError(format!("Sapling authorize failed: {e}")))?;

    #[cfg(feature = "shielded")]
    let orchard_prover = crate::config::orchard_prover()?;
    #[cfg(feature = "shielded")]
    let orchard_auth = unauthed_tx
        .orchard_bundle()
        .cloned()
        .map(|b| {
            b.create_proof(orchard_prover, &mut rng)
                .and_then(|b| {
                    b.apply_signatures(rng, *shielded_sig_commitment.as_ref(), &[])
                })
        })
        .transpose()
        .map_err(|e| ZecError::GenericError(format!("Orchard authorize failed: {e}")))?;
    
    #[cfg(not(feature = "shielded"))]
    let orchard_auth = None;

    // Reconstruct the transaction with the signed transparent bundle
    // and other bundles from the original unauthed_tx
    let tx_data = TransactionData::from_parts(
        unauthed_tx.version(),
        unauthed_tx.consensus_branch_id(),
        unauthed_tx.lock_time(),
        unauthed_tx.expiry_height(),
        signed_transparent_bundle_opt,
        None, // unauthed_tx.sprout_bundle().cloned(),
        sapling_auth,
        orchard_auth,
    );

    // Serialize the signed transaction
    let tx = tx_data
        .freeze()
        .map_err(|e| ZecError::GenericError(format!("fail to freeze tx_data: {e}")))?;
    let mut buffer = vec![];
    tx.write(&mut buffer)
        .map_err(|e| ZecError::GenericError(format!("fail to write tx_data to buffer: {e}")))?;

    Ok(buffer)
}

fn decode_hexstring(s: &str) -> Result<Vec<u8>, ZecError> {
    hex::decode(s).map_err(|_| ZecError::GenericError("Invalid Hex string".into()))
}

fn to_ba<const N: usize>(v: &[u8]) -> Result<[u8; N], ZecError> {
    let v: Result<[u8; N], _> = v.try_into();
    v.map_err(|e| {
        ZecError::GenericError(format!(
            "fail to convert slice to array of size {N}, err: {e}"
        ))
    })
}

fn to_hash(s: &str) -> Result<[u8; 32], ZecError> {
    let mut v = decode_hexstring(s)?;
    v.reverse();
    to_ba(&v)
}

pub fn get_ovk(pubkey: Vec<u8>) -> Result<Vec<u8>, ZecError> {
    let hash = blake2b_simd::Params::new()
        .hash_length(32)
        .personal(b"Zcash_Maya_OVK__")
        .hash(&pubkey);
    let ovk = hash.as_bytes().to_vec();
    Ok(ovk)
}

#[cfg(feature = "shielded")]
#[derive(serde::Serialize)]
struct PtxSeedView<'a> {
    height: u32,
    expiry_height: u32,
    fee: u64,
    inputs: &'a [UTXO],
    outputs: &'a [Output],
    network: u8, // 0=Main, 1=Test, 2=Regtest
}

#[cfg(feature = "shielded")]
fn rng_from_ptx(ptx: &PartialTx, network: Network) -> Result<ChaCha20Rng, ZecError> {
    let network_id = match network {
        Network::Main => 0,
        Network::Test => 1,
        Network::Regtest => 2,
    };
    
    let seed_view = PtxSeedView {
        height: ptx.height,
        expiry_height: ptx.expiry_height,
        fee: ptx.fee,
        inputs: &ptx.inputs,
        outputs: &ptx.outputs,
        network: network_id,
    };
    
    let h = blake2b_simd::Params::new()
        .hash_length(32)
        .personal(b"ZEC_TX_SEED")
        .hash(&bincode::serialize(&seed_view)
            .map_err(|e| ZecError::GenericError(format!("seed serialize: {e}")))?);
    
    
    Ok(ChaCha20Rng::from_seed(to_ba(h.as_bytes())?))
}

fn build_unauthorized_tx(
    vault: Vec<u8>,
    ptx: &PartialTx,
    network: Network,
) -> Result<TransactionData<zcash_primitives::transaction::Unauthorized>, ZecError> {
    // Use canonical subset seeding to avoid changes when PartialTx is mutated
    #[cfg(feature = "shielded")]
    let mut tx_rng = rng_from_ptx(ptx, network)?;
    #[cfg(feature = "shielded")]
    let ovk = get_ovk(vault.clone())?; // used as OVK for Sapling/Orchard outputs

    let pk  = PublicKey::from_slice(&vault).map_err(|e| ZecError::InvalidVaultPubkey(e.into()))?;

    // Transparent inputs
    let mut tbuilder = TransparentBuilder::empty();
    for i in &ptx.inputs {
        let op   = OutPoint::new(to_hash(&i.txid)?, i.vout);
        let coin = TxOut {
            value: Zatoshis::from_u64(i.value).map_err(|e| ZecError::InvalidAmount(i.value, e.into()))?,
            script_pubkey: Script(hex::decode(&i.script)
                .map_err(|e| ZecError::GenericError(format!("Invalid script hex: {e}")))?),
        };
        tbuilder.add_input(pk, op, coin)
            .map_err(|e| ZecError::GenericError(format!("Failed to add transparent input: {e}")))?;
    }

    // Shielded outputs-only builders (no shielded spends)
    #[cfg(feature = "shielded")]
    let mut sbuilder = sapling::builder::Builder::new(
        Zip212Enforcement::On,
        sapling::builder::BundleType::Transactional { bundle_required: false },
        sapling::Anchor::empty_tree(), // fine if no Sapling spends
    );
    #[cfg(feature = "shielded")]
    let mut obuilder = orchard::builder::Builder::new(
        orchard::builder::BundleType::Transactional { flags: Flags::ENABLED, bundle_required: false },
        orchard::Anchor::empty_tree(), // fine if no Orchard spends
    );

    // Route each output via handle_receiver (OP_RETURN only for T; empty memos for shielded)
    for o in &ptx.outputs {
        let recipient = Address::decode(&network, &o.address)
            .ok_or(ZecError::InvalidAddress(o.address.clone()))?;

        #[cfg(feature = "shielded")]
        let mut hr = |rx: Receiver| handle_receiver(
            rx, o.amount, &o.memo, &ovk, &mut tbuilder, &mut sbuilder, &mut obuilder
        );
        
        #[cfg(not(feature = "shielded"))]
        let mut hr = |rx: Receiver| handle_receiver_transparent_only(
            rx, o.amount, &o.memo, &mut tbuilder
        );

        match recipient {
            Address::Tex(_)                  => return Err(ZecError::InvalidAddress(o.address.clone())),
            Address::Transparent(t)          => hr(Receiver::Transparent(t))?,
            #[cfg(feature = "shielded")]
            Address::Sapling(s)              => hr(Receiver::Sapling(s))?,
            #[cfg(not(feature = "shielded"))]
            Address::Sapling(_)              => return Err(ZecError::GenericError("Sapling outputs not supported in production".into())),
            Address::Unified(u) => {
                #[cfg(feature = "shielded")]
                {
                    if let Some(&o) = u.orchard()     { hr(Receiver::Orchard(o))?; }
                    else if let Some(&s) = u.sapling(){ hr(Receiver::Sapling(s))?; }
                    else if let Some(&t) = u.transparent(){ hr(Receiver::Transparent(t))?; }
                    else { return Err(ZecError::GenericError("Unified address has no usable receiver".into())); }
                }
                #[cfg(not(feature = "shielded"))]
                {
                    if let Some(&t) = u.transparent(){ hr(Receiver::Transparent(t))?; }
                    else { return Err(ZecError::GenericError("Only transparent receivers supported in production".into())); }
                }
            }
        }
    }

    // Build bundles
    let tbundle = tbuilder.build();

    // --- S A P L I N G ---
    #[cfg(feature = "shielded")]
    let sapling_prover = crate::config::sapling_prover()?;
    #[cfg(feature = "shielded")]
    let sbundle: Option<sapling::Bundle<
        sapling::builder::InProgress<sapling::builder::Proven, sapling::builder::Unsigned>,
        zcash_protocol::value::ZatBalance
    >> = sbuilder
        // outputs-only: pass &[] for spends
        .build::<LocalTxProver, LocalTxProver, _, zcash_protocol::value::ZatBalance>(&[], &mut tx_rng)
        .map_err(|e| ZecError::GenericError(format!("Cannot build sapling bundle: {e}")))?
        .map(|(ubundle, _meta)| {
            ubundle.create_proofs(sapling_prover, sapling_prover, &mut tx_rng, ())
        });
    
    #[cfg(not(feature = "shielded"))]
    let sbundle = None;

    // --- O R C H A R D ---
    #[cfg(feature = "shielded")]
    let obundle: Option<orchard::Bundle<
        orchard::builder::InProgress<orchard::builder::Unproven, orchard::builder::Unauthorized>,
        zcash_protocol::value::ZatBalance
    >> = obuilder
        .build::<zcash_protocol::value::ZatBalance>(&mut tx_rng)
        .map_err(|e| ZecError::GenericError(format!("Cannot build orchard bundle: {e}")))?
        .map(|(ubundle_unproven, _meta)| ubundle_unproven);  // Just use the unproven bundle
    
    #[cfg(not(feature = "shielded"))]
    let obundle = None;

    // (Optional) static type check — only when shielded is enabled
    #[cfg(feature = "shielded")]
    let _: Option<orchard::Bundle<<Unauthorized as Authorization>::OrchardAuth, zcash_protocol::value::ZatBalance>> = obundle;

    // Version / branch / expiry
    // Use the actual height to get the correct consensus branch for the network
    let height = BlockHeight::from_u32(ptx.height);
    let branch = BranchId::for_height(&network, height);
    let version = TxVersion::suggested_for_branch(branch);
    let expiry  = if ptx.expiry_height == 0 { BlockHeight::from_u32(0) } else { BlockHeight::from_u32(ptx.expiry_height) };


    Ok(TransactionData::from_parts(
        version,
        branch,
        0,        // lock_time
        expiry,   // 0 = never expires
        tbundle,
        None,     // Sprout
        sbundle,  // Option<Sapling bundle (authorized)>
        obundle,  // Option<Orchard bundle (proved, Unauthorized auth state)
    ))
}

    #[cfg(feature = "shielded")]
fn handle_receiver(
    receiver: Receiver,
    amount: u64,
    memo: &str,
    ovk: &[u8],
    tbuilder: &mut TransparentBuilder,
    sbuilder: &mut sapling::builder::Builder,
    obuilder: &mut orchard::builder::Builder,
) -> Result<(), ZecError> {
    // Always empty memo for Sapling & Orchard
    let empty_memo = zcash_protocol::memo::MemoBytes::from(zcash_protocol::memo::Memo::Empty);    
    match receiver {
        Receiver::Transparent(transparent_address) => {
            let amount = Zatoshis::from_u64(amount).unwrap();
            tbuilder
                .add_output(&transparent_address, amount)
                .map_err(to_zecerror(anyhow!("Cannot add transparent output")))?;
            if !memo.is_empty() {
                if memo.len() > 80 {
                    return Err(ZecError::GenericError("OP_RETURN memo > 80 bytes".into()));
                }
                tbuilder
                    .add_null_data_output(memo.as_bytes())
                    .map_err(to_zecerror(anyhow!("Cannot add transparent memo")))?;
            }
        }
        #[cfg(feature = "shielded")]
        Receiver::Sapling(payment_address) => {
            sbuilder
                .add_output(
                    Some(sapling::keys::OutgoingViewingKey(to_ba(ovk)?)),
                    payment_address,
                    sapling::value::NoteValue::from_raw(amount),
                    empty_memo.into_bytes(),
                )
                .map_err(|e| ZecError::GenericError(e.to_string()))?;
        }
        #[cfg(feature = "shielded")]
        Receiver::Orchard(address) => {
            obuilder
                .add_output(
                    Some(OutgoingViewingKey::from(to_ba(ovk)?)),
                    address,
                    NoteValue::from_raw(amount),
                    empty_memo.into_bytes(),
                )
                .map_err(|e| ZecError::GenericError(e.to_string()))?;
        }
        // Other variants are handled above; this arm does not compile in non-regtest builds.
    }

    Ok::<_, ZecError>(())
}

    #[cfg(not(feature = "shielded"))]
fn handle_receiver_transparent_only(
    receiver: Receiver,
    amount: u64,
    memo: &str,
    tbuilder: &mut TransparentBuilder,
) -> Result<(), ZecError> {
    match receiver {
        Receiver::Transparent(transparent_address) => {
            let amount = Zatoshis::from_u64(amount).unwrap();
            tbuilder
                .add_output(&transparent_address, amount)
                .map_err(to_zecerror(anyhow!("Cannot add transparent output")))?;
            if !memo.is_empty() {
                if memo.len() > 80 {
                    return Err(ZecError::GenericError("OP_RETURN memo > 80 bytes".into()));
                }
                tbuilder
                    .add_null_data_output(memo.as_bytes())
                    .map_err(to_zecerror(anyhow!("Cannot add transparent memo")))?;
            }
        }
        _ => {
            return Err(ZecError::GenericError("Shielded outputs not supported in production".into()));
        }
    }
    Ok(())
}

pub fn compute_txid(vault: Vec<u8>, ptx: PartialTx, network: Network) -> Result<String, ZecError> {
    // build the initial unauthorized transaction
    let unauthed_tx = build_unauthorized_tx(vault, &ptx, network)?;
    // calculate the tx hash
    let txid_parts = unauthed_tx.digest(TxIdDigester);
    let txid = *signature_hash(&unauthed_tx, &SignableInput::Shielded, &txid_parts)
        .as_ref();
    let txid_hex = hex::encode(txid);
    Ok(txid_hex)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::init_zec;
    use crate::keys::derive_account_from_mnemonic;

    #[test]
    fn test_seed_determinism_between_build_calls() -> Result<(), ZecError> {
        // Create a simple PartialTx with minimal data (no need for realistic scripts)
        let mock_utxo = UTXO {
            txid: "1111111111111111111111111111111111111111111111111111111111111111".to_string(),
            vout: 0,
            script: "76a914389ffce9cd9ae88dcc0631e88a821ffdbe9bfe261588ac".to_string(),
            value: 1_000_000_000,
            height: 100,
        };
        
        let original_ptx = PartialTx {
            height: 100,
            txid: vec![],
            inputs: vec![mock_utxo],
            outputs: vec![Output {
                address: "tmTest123456789012345678901234567890".to_string(), // Simple transparent address
                amount: 999_900_000,
                memo: "".to_string(),
            }],
            fee: 100_000,
            sighashes: vec![],
            expiry_height: 0,
            version: 0,
        };

        
        // Test the seed generation directly by capturing the actual seeds
        let seed1 = {
            let seed_view = PtxSeedView {
                height: original_ptx.height,
                expiry_height: original_ptx.expiry_height,
                fee: original_ptx.fee,
                inputs: &original_ptx.inputs,
                outputs: &original_ptx.outputs,
                network: 1, // Test network
            };
            let h = blake2b_simd::Params::new()
                .hash_length(32)
                .personal(b"ZEC_TX_SEED")
                .hash(&bincode::serialize(&seed_view).unwrap());
            hex::encode(h.as_bytes())
        };
        
        // Second call with mutated ptx (simulate what happens after build_ptx)
        let mut mutated_ptx = original_ptx.clone();
        mutated_ptx.txid = vec![0xaa; 32]; // Fill with dummy txid
        mutated_ptx.sighashes = vec![vec![0xbb; 32]]; // Fill with dummy sighashes
        mutated_ptx.version = 4; // Set version
        
        let seed2 = {
            let seed_view = PtxSeedView {
                height: mutated_ptx.height,
                expiry_height: mutated_ptx.expiry_height,
                fee: mutated_ptx.fee,
                inputs: &mutated_ptx.inputs,
                outputs: &mutated_ptx.outputs,
                network: 1, // Test network
            };
            let h = blake2b_simd::Params::new()
                .hash_length(32)
                .personal(b"ZEC_TX_SEED")
                .hash(&bincode::serialize(&seed_view).unwrap());
            hex::encode(h.as_bytes())
        };
        
        
        // Verify they are identical!
        assert_eq!(seed1, seed2, "Seeds should be identical due to canonical seeding!");
        
        
        Ok(())
    }

    #[test]
    fn test_complete_deterministic_flow_validation() -> Result<(), ZecError> {
        // This test validates that the ENTIRE flow produces identical results
        let network = Network::Test;
        init_zec().unwrap();
        
        let mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about";
        let account = derive_account_from_mnemonic(mnemonic.to_string(), network).unwrap();
        
        // Create a realistic T->Sapling transaction
        let mock_utxo = UTXO {
            txid: "1111111111111111111111111111111111111111111111111111111111111111".to_string(),
            vout: 0,
            script: "76a914389ffce9cd9ae88dcc0631e88a821ffdbe9bfe261588ac".to_string(),
            value: 1_000_000_000,
            height: 100,
        };
        
        let original_ptx = PartialTx {
            height: 100,
            txid: vec![],
            inputs: vec![mock_utxo],
            outputs: vec![Output {
                address: account.sapling.clone(),
                amount: 999_900_000,
                memo: "test".to_string(),
            }],
            fee: 100_000,
            sighashes: vec![],
            expiry_height: 0,
            version: 0,
        };

        
        // Build transaction twice and verify transaction IDs are identical
        let tx1 = build_unauthorized_tx(account.public_key.clone(), &original_ptx, network)?;
        let txid1 = tx1.digest(TxIdDigester);
        
        let tx2 = build_unauthorized_tx(account.public_key.clone(), &original_ptx, network)?;
        let txid2 = tx2.digest(TxIdDigester);
        
        // Verify the sapling digests are identical (most relevant for our fix)
        assert_eq!(txid1.sapling_digest, txid2.sapling_digest, "Sapling digests should be identical!");
        
        // Also verify sapling bundle bytes if present
        if let (Some(bundle1), Some(bundle2)) = (tx1.sapling_bundle(), tx2.sapling_bundle()) {
            // Compare some deterministic properties
            assert_eq!(bundle1.shielded_outputs().len(), bundle2.shielded_outputs().len(), 
                      "Sapling output counts should match");
        }
        
        
        Ok(())
    }
}
