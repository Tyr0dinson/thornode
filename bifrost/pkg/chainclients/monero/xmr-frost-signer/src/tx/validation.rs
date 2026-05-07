use anyhow::{Context, Result};
use curve25519_dalek::{constants::ED25519_BASEPOINT_TABLE, EdwardsPoint, Scalar};
use monero_wallet::{
    address::MoneroAddress,
    extra::Extra,
    io::write_varint,
    primitives::{keccak256, keccak256_to_scalar, Commitment},
    ringct::EncryptedAmount,
    transaction::{Input, Output, Pruned, Timelock, Transaction},
};
#[cfg(test)]
use monero_wallet::{
    address::{AddressType, Network},
    extra::ExtraField,
    ringct::{PrunedRctProofs, RctBase, RctType},
    transaction::TransactionPrefix,
};
use zeroize::Zeroizing;

#[derive(Clone)]
struct OutputDerivations {
    view_tag: u8,
    shared_key: Scalar,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ValidatedOutboundTransaction {
    pub tracked_owned_output: bool,
    pub actual_fee: u64,
    pub destination_amount: u64,
}

#[derive(Debug, Clone)]
pub struct MaterializeOwnedAddress {
    pub address: String,
    pub subaddress_major: u32,
    pub subaddress_minor: u32,
}

#[derive(Debug, Clone)]
pub struct MaterializedOwnedOutput {
    pub output_index: u32,
    pub amount: u64,
    pub address: String,
    pub subaddress_major: u32,
    pub subaddress_minor: u32,
    pub wallet_output_hex: String,
}

pub fn validate_outbound_transaction(
    tx_hex: &str,
    tx_keys: &[String],
    to_address: &str,
    expected_amount: u64,
    expected_fee: Option<u64>,
    owned_addresses: &[String],
) -> Result<ValidatedOutboundTransaction> {
    validate_outbound_transaction_inner(
        tx_hex,
        tx_keys,
        to_address,
        Some(expected_amount),
        expected_fee,
        owned_addresses,
    )
}

pub fn validate_registered_outbound_transaction(
    tx_hex: &str,
    tx_keys: &[String],
    to_address: &str,
    expected_fee: Option<u64>,
    owned_addresses: &[String],
) -> Result<ValidatedOutboundTransaction> {
    validate_outbound_transaction_inner(
        tx_hex,
        tx_keys,
        to_address,
        None,
        expected_fee,
        owned_addresses,
    )
}

fn validate_outbound_transaction_inner(
    tx_hex: &str,
    tx_keys: &[String],
    to_address: &str,
    expected_amount: Option<u64>,
    expected_fee: Option<u64>,
    owned_addresses: &[String],
) -> Result<ValidatedOutboundTransaction> {
    if tx_keys.is_empty() {
        anyhow::bail!("tx_keys must not be empty");
    }
    let tx = parse_pruned_transaction(tx_hex)?;
    let Transaction::V2 {
        prefix,
        proofs: Some(proofs),
    } = tx
    else {
        anyhow::bail!("expected a v2 RingCT transaction with proofs");
    };

    let fee = proofs.base.fee;
    if let Some(expected_fee) = expected_fee {
        let input_count = prefix.inputs.len().max(1) as u64;
        let fee_cap = expected_fee.saturating_mul(input_count);
        if fee > fee_cap {
            anyhow::bail!("fee mismatch: expected at most {fee_cap} for {input_count} inputs ({expected_fee} per input), got {fee}");
        }
    }

    if prefix.outputs.len() != proofs.base.encrypted_amounts.len()
        || prefix.outputs.len() != proofs.base.commitments.len()
    {
        anyhow::bail!("transaction outputs and RingCT base are inconsistent");
    }

    let address = MoneroAddress::from_str_with_unchecked_network(to_address)
        .with_context(|| format!("invalid destination address: {to_address}"))?;
    let owned_addresses = owned_addresses
        .iter()
        .map(|address| {
            MoneroAddress::from_str_with_unchecked_network(address)
                .with_context(|| format!("invalid owned address: {address}"))
        })
        .collect::<Result<Vec<_>>>()?;
    let (primary_keys, additional_keys) = extract_tx_public_keys(&prefix.extra)?;
    let parsed_tx_keys = tx_keys
        .iter()
        .map(|tx_key| parse_tx_key(tx_key))
        .collect::<Result<Vec<_>>>()?;

    match &additional_keys {
        Some(additional_keys) => {
            if parsed_tx_keys.len() != 1 + additional_keys.len() {
                anyhow::bail!(
                    "tx_keys length mismatch: expected {}, got {}",
                    1 + additional_keys.len(),
                    parsed_tx_keys.len()
                );
            }
        }
        None => {
            if parsed_tx_keys.len() != 1 {
                anyhow::bail!("unexpected additional tx_keys without tx extra additional keys");
            }
        }
    }

    let expected_primary_public = if address.is_subaddress() && additional_keys.is_none() {
        parsed_tx_keys[0] * address.spend()
    } else {
        &parsed_tx_keys[0] * ED25519_BASEPOINT_TABLE
    };
    if primary_keys[0] != expected_primary_public {
        anyhow::bail!("primary tx key does not match transaction extra");
    }

    let mut matched_destination_total = 0u128;
    let mut matched_owned = owned_addresses.is_empty();
    for (index, output) in prefix.outputs.iter().enumerate() {
        let tx_key_candidates = tx_key_candidates_for_output(
            &address,
            &parsed_tx_keys,
            additional_keys.as_deref(),
            index,
        )?;
        for tx_key in tx_key_candidates {
            if let Some(amount) = matched_output_amount(
                &prefix.inputs,
                output,
                &proofs.base.encrypted_amounts[index],
                &proofs.base.commitments[index],
                &address,
                tx_key,
                index,
            )? {
                matched_destination_total = matched_destination_total
                    .checked_add(u128::from(amount))
                    .ok_or_else(|| anyhow::anyhow!("matched destination amount overflow"))?;
                break;
            }
        }

        if matched_owned {
            continue;
        }
        'owned: for owned_address in &owned_addresses {
            let owned_candidates = tx_key_candidates_for_output(
                owned_address,
                &parsed_tx_keys,
                additional_keys.as_deref(),
                index,
            )?;
            for tx_key in owned_candidates {
                if matches_output(
                    &prefix.inputs,
                    output,
                    &proofs.base.encrypted_amounts[index],
                    &proofs.base.commitments[index],
                    owned_address,
                    tx_key,
                    index,
                    None,
                )? {
                    matched_owned = true;
                    break 'owned;
                }
            }
        }
    }

    if matched_destination_total == 0 {
        anyhow::bail!("no transaction output matched the provided tx key(s) and destination");
    }
    if let Some(expected_amount) = expected_amount {
        if matched_destination_total != u128::from(expected_amount) {
            anyhow::bail!(
                "destination amount mismatch: expected {}, matched {}",
                expected_amount,
                matched_destination_total
            );
        }
    }
    let destination_amount = u64::try_from(matched_destination_total)
        .map_err(|_| anyhow::anyhow!("matched destination amount exceeds u64"))?;
    Ok(ValidatedOutboundTransaction {
        tracked_owned_output: matched_owned,
        actual_fee: fee,
        destination_amount,
    })
}

fn parse_pruned_transaction(tx_hex: &str) -> Result<Transaction<Pruned>> {
    let tx_hex = tx_hex.trim();
    if tx_hex.is_empty() {
        anyhow::bail!("tx_hex must not be empty");
    }
    let bytes = hex::decode(tx_hex).context("tx_hex is not valid hex")?;
    Transaction::<Pruned>::read(&mut bytes.as_slice())
        .context("failed to decode pruned transaction")
}

pub fn materialize_owned_outputs_from_tx_keys(
    tx_hash: &str,
    tx_hex: &str,
    tx_keys: &[String],
    owned_addresses: &[MaterializeOwnedAddress],
    output_index_for_first_ringct_output: u64,
) -> Result<Vec<MaterializedOwnedOutput>> {
    if tx_keys.is_empty() {
        anyhow::bail!("tx_keys must not be empty");
    }
    let tx_hash_bytes: [u8; 32] = hex::decode(tx_hash.trim())
        .context("tx_hash is not valid hex")?
        .try_into()
        .map_err(|_| anyhow::anyhow!("tx_hash must be 32 bytes"))?;
    let tx = parse_pruned_transaction(tx_hex)?;
    let Transaction::V2 {
        prefix,
        proofs: Some(proofs),
    } = tx
    else {
        anyhow::bail!("expected a v2 RingCT transaction with proofs");
    };
    if prefix.outputs.len() != proofs.base.encrypted_amounts.len()
        || prefix.outputs.len() != proofs.base.commitments.len()
    {
        anyhow::bail!("transaction outputs and RingCT base are inconsistent");
    }

    let (_primary_keys, additional_keys) = extract_tx_public_keys(&prefix.extra)?;
    let parsed_tx_keys = tx_keys
        .iter()
        .map(|tx_key| parse_tx_key(tx_key))
        .collect::<Result<Vec<_>>>()?;
    match &additional_keys {
        Some(additional_keys) => {
            if parsed_tx_keys.len() != 1 + additional_keys.len() {
                anyhow::bail!(
                    "tx_keys length mismatch: expected {}, got {}",
                    1 + additional_keys.len(),
                    parsed_tx_keys.len()
                );
            }
        }
        None => {
            if parsed_tx_keys.len() != 1 {
                anyhow::bail!("unexpected additional tx_keys without tx extra additional keys");
            }
        }
    }

    let owned = owned_addresses
        .iter()
        .map(|entry| {
            Ok((
                entry,
                MoneroAddress::from_str_with_unchecked_network(&entry.address)
                    .with_context(|| format!("invalid owned address: {}", entry.address))?,
            ))
        })
        .collect::<Result<Vec<_>>>()?;
    let extra_data = Extra::read::<&[u8]>(&mut prefix.extra.as_ref())
        .ok()
        .map(|extra| extra.data())
        .unwrap_or_default();
    let mut out = Vec::new();

    for (index, output) in prefix.outputs.iter().enumerate() {
        'owned: for (entry, address) in &owned {
            let owned_candidates = tx_key_candidates_for_output(
                address,
                &parsed_tx_keys,
                additional_keys.as_deref(),
                index,
            )?;
            for tx_key in owned_candidates {
                let derivations = output_derivations(
                    address.is_guaranteed().then(|| uniqueness(&prefix.inputs)),
                    Zeroizing::new(address.view() * tx_key),
                    index,
                );
                if output
                    .view_tag
                    .map(|view_tag| view_tag != derivations.view_tag)
                    .unwrap_or(false)
                {
                    continue;
                }
                let Some(output_key) = output.key.decompress() else {
                    continue;
                };
                let expected_output_key = (address.spend()
                    + (&derivations.shared_key * ED25519_BASEPOINT_TABLE))
                    .compress();
                if output.key != expected_output_key {
                    continue;
                }
                let commitment =
                    decrypt_commitment(&derivations, &proofs.base.encrypted_amounts[index]);
                if commitment.calculate() != proofs.base.commitments[index] {
                    continue;
                }

                let subaddress = (entry.subaddress_major, entry.subaddress_minor);
                let mut key_offset = derivations.shared_key;
                if subaddress != (0, 0) {
                    key_offset += subaddress_derivation(subaddress.0, subaddress.1);
                }
                let output_index = u32::try_from(index)
                    .map_err(|_| anyhow::anyhow!("output index exceeds u32"))?;
                let absolute_index = output_index_for_first_ringct_output
                    .checked_add(index as u64)
                    .ok_or_else(|| anyhow::anyhow!("blockchain output index overflow"))?;

                out.push(MaterializedOwnedOutput {
                    output_index,
                    amount: commitment.amount,
                    address: entry.address.clone(),
                    subaddress_major: entry.subaddress_major,
                    subaddress_minor: entry.subaddress_minor,
                    wallet_output_hex: hex::encode(serialize_wallet_output(
                        tx_hash_bytes,
                        index as u64,
                        absolute_index,
                        output_key,
                        key_offset,
                        &commitment,
                        Some(subaddress),
                        &extra_data,
                    )),
                });
                break 'owned;
            }
        }
    }

    Ok(out)
}

fn subaddress_derivation(account: u32, address: u32) -> Scalar {
    let view = crate::xmr::additional_view_scalar();
    keccak256_to_scalar(Zeroizing::new(
        [
            b"SubAddr\0".as_ref(),
            Zeroizing::new(view.to_bytes()).as_ref(),
            &account.to_le_bytes(),
            &address.to_le_bytes(),
        ]
        .concat(),
    ))
}

fn serialize_wallet_output(
    tx_hash: [u8; 32],
    index_in_transaction: u64,
    index_on_blockchain: u64,
    output_key: EdwardsPoint,
    key_offset: Scalar,
    commitment: &Commitment,
    subaddress: Option<(u32, u32)>,
    arbitrary_data: &[Vec<u8>],
) -> Vec<u8> {
    let mut bytes = Vec::with_capacity(160);
    bytes.extend(tx_hash);
    bytes.extend(index_in_transaction.to_le_bytes());
    bytes.extend(index_on_blockchain.to_le_bytes());
    bytes.extend(output_key.compress().to_bytes());
    bytes.extend(key_offset.to_bytes());
    commitment
        .write(&mut bytes)
        .expect("write failed but Vec<u8> cannot fail");
    Timelock::None
        .write(&mut bytes)
        .expect("write failed but Vec<u8> cannot fail");
    if let Some((major, minor)) = subaddress {
        bytes.push(1);
        bytes.extend(major.to_le_bytes());
        bytes.extend(minor.to_le_bytes());
    } else {
        bytes.push(0);
    }
    bytes.push(0);
    bytes.extend((arbitrary_data.len() as u64).to_le_bytes());
    for chunk in arbitrary_data {
        let len = u8::try_from(chunk.len()).unwrap_or(0);
        bytes.push(len);
        bytes.extend(&chunk[..usize::from(len)]);
    }
    bytes
}

fn extract_tx_public_keys(
    extra_bytes: &[u8],
) -> Result<(Vec<EdwardsPoint>, Option<Vec<EdwardsPoint>>)> {
    let extra =
        Extra::read::<&[u8]>(&mut extra_bytes.as_ref()).context("failed to decode tx extra")?;
    let Some((primary_keys, additional_keys)) = extra.keys() else {
        anyhow::bail!("transaction extra does not contain tx public keys");
    };
    if primary_keys.len() != 1 {
        anyhow::bail!("expected exactly one primary tx public key");
    }
    Ok((primary_keys, additional_keys))
}

fn parse_tx_key(tx_key_hex: &str) -> Result<Scalar> {
    let normalized = tx_key_hex.trim().to_ascii_lowercase();
    if normalized.len() != 64 {
        anyhow::bail!("tx key must be a 32-byte hex string");
    }
    let bytes = hex::decode(&normalized).context("tx key is not valid hex")?;
    Ok(Scalar::from_bytes_mod_order(
        bytes
            .try_into()
            .expect("decoded tx key length is already validated"),
    ))
}

fn tx_key_candidates_for_output<'a>(
    address: &MoneroAddress,
    parsed_tx_keys: &'a [Scalar],
    additional_keys: Option<&[EdwardsPoint]>,
    output_index: usize,
) -> Result<Vec<&'a Scalar>> {
    let mut candidates = vec![&parsed_tx_keys[0]];
    let Some(additional_keys) = additional_keys else {
        return Ok(candidates);
    };
    let Some(additional_public) = additional_keys.get(output_index) else {
        return Ok(candidates);
    };
    let additional_private = &parsed_tx_keys[output_index + 1];
    let expected_additional_public = if address.is_subaddress() {
        additional_private * address.spend()
    } else {
        additional_private * ED25519_BASEPOINT_TABLE
    };
    if *additional_public == expected_additional_public {
        candidates.push(additional_private);
    }
    Ok(candidates)
}

fn matches_output(
    inputs: &[Input],
    output: &Output,
    encrypted_amount: &EncryptedAmount,
    commitment_point: &EdwardsPoint,
    address: &MoneroAddress,
    tx_key: &Scalar,
    output_index: usize,
    expected_amount: Option<u64>,
) -> Result<bool> {
    let Some(amount) = matched_output_amount(
        inputs,
        output,
        encrypted_amount,
        commitment_point,
        address,
        tx_key,
        output_index,
    )?
    else {
        return Ok(false);
    };
    if let Some(expected_amount) = expected_amount {
        if amount != expected_amount {
            return Ok(false);
        }
    }
    Ok(true)
}

fn matched_output_amount(
    inputs: &[Input],
    output: &Output,
    encrypted_amount: &EncryptedAmount,
    commitment_point: &EdwardsPoint,
    address: &MoneroAddress,
    tx_key: &Scalar,
    output_index: usize,
) -> Result<Option<u64>> {
    let derivations = output_derivations(
        address.is_guaranteed().then(|| uniqueness(inputs)),
        Zeroizing::new(address.view() * tx_key),
        output_index,
    );
    if output
        .view_tag
        .map(|view_tag| view_tag != derivations.view_tag)
        .unwrap_or(false)
    {
        return Ok(None);
    }

    let expected_output_key =
        (address.spend() + (&derivations.shared_key * ED25519_BASEPOINT_TABLE)).compress();
    if output.key != expected_output_key {
        return Ok(None);
    }

    let commitment = decrypt_commitment(&derivations, encrypted_amount);
    if commitment.calculate() != *commitment_point {
        return Ok(None);
    }
    Ok(Some(commitment.amount))
}

fn uniqueness(inputs: &[Input]) -> [u8; 32] {
    let mut encoded = b"uniqueness".to_vec();
    for input in inputs {
        match input {
            Input::Gen(height) => {
                write_varint(height, &mut encoded).expect("write failed but Vec<u8> cannot fail");
            }
            Input::ToKey { key_image, .. } => encoded.extend(key_image.compress().to_bytes()),
        }
    }
    keccak256(encoded)
}

fn output_derivations(
    uniqueness: Option<[u8; 32]>,
    ecdh: Zeroizing<EdwardsPoint>,
    output_index: usize,
) -> OutputDerivations {
    let mut output_derivation =
        Zeroizing::new(ecdh.mul_by_cofactor().compress().to_bytes().to_vec());
    write_varint(&output_index, &mut *output_derivation)
        .expect("write failed but Vec<u8> cannot fail");

    let view_tag = keccak256([b"view_tag".as_ref(), output_derivation.as_ref()].concat())[0];
    let hash_material = if let Some(uniqueness) = uniqueness {
        [uniqueness.as_ref(), output_derivation.as_ref()].concat()
    } else {
        output_derivation.to_vec()
    };

    OutputDerivations {
        view_tag,
        shared_key: keccak256_to_scalar(&hash_material),
    }
}

fn decrypt_commitment(
    derivations: &OutputDerivations,
    encrypted_amount: &EncryptedAmount,
) -> Commitment {
    match encrypted_amount {
        EncryptedAmount::Original { mask, amount } => {
            let mask_shared_secret = keccak256_to_scalar(derivations.shared_key.as_bytes());
            let amount_shared_secret = keccak256_to_scalar(mask_shared_secret.as_bytes());
            let mask = Scalar::from_bytes_mod_order(*mask) - mask_shared_secret;
            let amount_scalar = Scalar::from_bytes_mod_order(*amount) - amount_shared_secret;
            let amount = u64::from_le_bytes(
                amount_scalar.to_bytes()[..8]
                    .try_into()
                    .expect("32-byte scalar bytes must expose an 8-byte prefix"),
            );
            Commitment::new(mask, amount)
        }
        EncryptedAmount::Compact { amount } => {
            let mut amount_mask = Zeroizing::new(b"amount".to_vec());
            amount_mask.extend(derivations.shared_key.to_bytes());
            let amount_mask = keccak256(&amount_mask);
            let mut amount_mask_8 = [0u8; 8];
            amount_mask_8.copy_from_slice(&amount_mask[..8]);
            Commitment::new(
                commitment_mask(derivations),
                u64::from_le_bytes(*amount) ^ u64::from_le_bytes(amount_mask_8),
            )
        }
    }
}

fn commitment_mask(derivations: &OutputDerivations) -> Scalar {
    let mut encoded = b"commitment_mask".to_vec();
    encoded.extend(derivations.shared_key.to_bytes());
    keccak256_to_scalar(&encoded)
}

#[cfg(test)]
#[derive(Clone)]
#[allow(dead_code)]
pub(crate) struct SyntheticOutboundCase {
    pub tx_hex: String,
    pub tx_keys: Vec<String>,
    pub to_address: String,
    pub amount: u64,
    pub fee: u64,
}

#[cfg(test)]
fn point_from_scalar(value: u64) -> EdwardsPoint {
    &Scalar::from(value) * ED25519_BASEPOINT_TABLE
}

#[cfg(test)]
fn scalar_hex(value: u64) -> String {
    hex::encode(Scalar::from(value).to_bytes())
}

#[cfg(test)]
struct SyntheticOutput {
    output: Output,
    encrypted_amount: EncryptedAmount,
    commitment: EdwardsPoint,
}

#[cfg(test)]
fn synthetic_output_for_address(
    tx_key: &Scalar,
    output_index: usize,
    address: &MoneroAddress,
    amount: u64,
) -> SyntheticOutput {
    let derivations =
        output_derivations(None, Zeroizing::new(address.view() * tx_key), output_index);
    let mask = commitment_mask(&derivations);
    let mut amount_mask = Zeroizing::new(b"amount".to_vec());
    amount_mask.extend(derivations.shared_key.to_bytes());
    let amount_mask = keccak256(&amount_mask);
    let mut amount_mask_8 = [0u8; 8];
    amount_mask_8.copy_from_slice(&amount_mask[..8]);

    let commitment = Commitment::new(mask, amount).calculate();
    SyntheticOutput {
        output: Output {
            amount: None,
            key: (address.spend() + (&derivations.shared_key * ED25519_BASEPOINT_TABLE)).compress(),
            view_tag: Some(derivations.view_tag),
        },
        encrypted_amount: EncryptedAmount::Compact {
            amount: (amount ^ u64::from_le_bytes(amount_mask_8)).to_le_bytes(),
        },
        commitment,
    }
}

#[cfg(test)]
fn serialize_extra(primary: EdwardsPoint, additional: Vec<EdwardsPoint>) -> Vec<u8> {
    let mut extra = ExtraField::PublicKey(primary).serialize();
    if !additional.is_empty() {
        extra.extend(ExtraField::PublicKeys(additional).serialize());
    }
    extra
}

#[cfg(test)]
fn synthetic_pruned_tx_hex_with_inputs(
    fee: u64,
    extra: Vec<u8>,
    outputs: Vec<SyntheticOutput>,
    input_count: usize,
) -> String {
    let tx = Transaction::<Pruned>::V2 {
        prefix: TransactionPrefix {
            additional_timelock: Timelock::None,
            inputs: (0..input_count.max(1))
                .map(|i| Input::ToKey {
                    amount: None,
                    key_offsets: vec![1],
                    key_image: point_from_scalar(99 + i as u64),
                })
                .collect(),
            outputs: outputs.iter().map(|output| output.output.clone()).collect(),
            extra,
        },
        proofs: Some(PrunedRctProofs {
            rct_type: RctType::ClsagBulletproofPlus,
            base: RctBase {
                fee,
                pseudo_outs: vec![],
                encrypted_amounts: outputs
                    .iter()
                    .map(|output| output.encrypted_amount.clone())
                    .collect(),
                commitments: outputs.iter().map(|output| output.commitment).collect(),
            },
        }),
    };
    hex::encode(tx.serialize())
}

#[cfg(test)]
fn synthetic_pruned_tx_hex(fee: u64, extra: Vec<u8>, outputs: Vec<SyntheticOutput>) -> String {
    synthetic_pruned_tx_hex_with_inputs(fee, extra, outputs, 1)
}

#[cfg(test)]
#[allow(dead_code)]
pub(crate) fn synthetic_untracked_outbound_case_for_tests() -> SyntheticOutboundCase {
    let recipient = MoneroAddress::new(
        Network::Mainnet,
        AddressType::Legacy,
        point_from_scalar(11),
        point_from_scalar(13),
    );
    let unrelated = MoneroAddress::new(
        Network::Mainnet,
        AddressType::Legacy,
        point_from_scalar(17),
        point_from_scalar(19),
    );
    let primary = Scalar::from(7u64);
    let fee = 42u64;
    let amount = 1_000_000u64;
    let tx_hex = synthetic_pruned_tx_hex(
        fee,
        serialize_extra(&primary * ED25519_BASEPOINT_TABLE, vec![]),
        vec![
            synthetic_output_for_address(&primary, 0, &recipient, amount),
            synthetic_output_for_address(&primary, 1, &unrelated, 2_000_000),
        ],
    );

    SyntheticOutboundCase {
        tx_hex,
        tx_keys: vec![scalar_hex(7)],
        to_address: recipient.to_string(),
        amount,
        fee,
    }
}

#[cfg(test)]
#[allow(dead_code)]
pub(crate) fn synthetic_owned_change_outbound_case_for_tests(
    owned_change_address: &str,
) -> SyntheticOutboundCase {
    let recipient = MoneroAddress::new(
        Network::Mainnet,
        AddressType::Legacy,
        point_from_scalar(11),
        point_from_scalar(13),
    );
    let change = MoneroAddress::from_str_with_unchecked_network(owned_change_address)
        .expect("test owned change address should parse");
    let primary = Scalar::from(7u64);
    let fee = 42u64;
    let amount = 1_000_000u64;
    let tx_hex = synthetic_pruned_tx_hex(
        fee,
        serialize_extra(&primary * ED25519_BASEPOINT_TABLE, vec![]),
        vec![
            synthetic_output_for_address(&primary, 0, &recipient, amount),
            synthetic_output_for_address(&primary, 1, &change, 2_000_000),
        ],
    );

    SyntheticOutboundCase {
        tx_hex,
        tx_keys: vec![scalar_hex(7)],
        to_address: recipient.to_string(),
        amount,
        fee,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn validate_standard_outbound_transaction_succeeds() {
        let recipient = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(11),
            point_from_scalar(13),
        );
        let change = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(17),
            point_from_scalar(19),
        );
        let primary = Scalar::from(7u64);
        let fee = 42u64;
        let tx_hex = synthetic_pruned_tx_hex(
            fee,
            serialize_extra(&primary * ED25519_BASEPOINT_TABLE, vec![]),
            vec![
                synthetic_output_for_address(&primary, 0, &recipient, 1_000_000),
                synthetic_output_for_address(&primary, 1, &change, 2_000_000),
            ],
        );

        let validated = validate_outbound_transaction(
            &tx_hex,
            &[scalar_hex(7)],
            &recipient.to_string(),
            1_000_000,
            Some(fee),
            &[change.to_string()],
        )
        .expect("standard outbound validation should succeed");
        assert!(validated.tracked_owned_output);
        assert_eq!(validated.actual_fee, fee);
    }

    #[test]
    fn validate_outbound_transaction_sums_multiple_destination_outputs() {
        let recipient = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(11),
            point_from_scalar(13),
        );
        let primary = Scalar::from(7u64);
        let fee = 42u64;
        let tx_hex = synthetic_pruned_tx_hex(
            fee,
            serialize_extra(&primary * ED25519_BASEPOINT_TABLE, vec![]),
            vec![
                synthetic_output_for_address(&primary, 0, &recipient, 400_000),
                synthetic_output_for_address(&primary, 1, &recipient, 600_000),
            ],
        );

        let validated = validate_outbound_transaction(
            &tx_hex,
            &[scalar_hex(7)],
            &recipient.to_string(),
            1_000_000,
            Some(fee),
            &[recipient.to_string()],
        )
        .expect("validation should sum both destination outputs");
        assert!(validated.tracked_owned_output);
        assert_eq!(validated.actual_fee, fee);
    }

    #[test]
    fn validate_registered_outbound_transaction_derives_same_address_total() {
        let recipient = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(11),
            point_from_scalar(13),
        );
        let primary = Scalar::from(7u64);
        let fee = 42u64;
        let first_output = 1_028_908_920_000u64;
        let second_output = 4_771_920_000u64;
        let tx_hex = synthetic_pruned_tx_hex(
            fee,
            serialize_extra(&primary * ED25519_BASEPOINT_TABLE, vec![]),
            vec![
                synthetic_output_for_address(&primary, 0, &recipient, first_output),
                synthetic_output_for_address(&primary, 1, &recipient, second_output),
            ],
        );

        let err = validate_outbound_transaction(
            &tx_hex,
            &[scalar_hex(7)],
            &recipient.to_string(),
            first_output,
            Some(fee),
            &[recipient.to_string()],
        )
        .expect_err("strict validation must compare the same-address total");
        assert!(
            err.to_string().contains(&format!(
                "destination amount mismatch: expected {first_output}, matched {}",
                first_output + second_output
            )),
            "unexpected error: {err}"
        );

        let validated = validate_registered_outbound_transaction(
            &tx_hex,
            &[scalar_hex(7)],
            &recipient.to_string(),
            Some(fee),
            &[recipient.to_string()],
        )
        .expect("registered inventory replay should derive the same-address total");
        assert_eq!(validated.destination_amount, first_output + second_output);
        assert!(validated.tracked_owned_output);
    }

    #[test]
    fn validate_standard_outbound_transaction_rejects_forged_tx_key() {
        let recipient = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(11),
            point_from_scalar(13),
        );
        let change = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(17),
            point_from_scalar(19),
        );
        let primary = Scalar::from(7u64);
        let fee = 42u64;
        let tx_hex = synthetic_pruned_tx_hex(
            fee,
            serialize_extra(&primary * ED25519_BASEPOINT_TABLE, vec![]),
            vec![
                synthetic_output_for_address(&primary, 0, &recipient, 1_000_000),
                synthetic_output_for_address(&primary, 1, &change, 2_000_000),
            ],
        );

        let err = validate_outbound_transaction(
            &tx_hex,
            &[scalar_hex(8)],
            &recipient.to_string(),
            1_000_000,
            Some(fee),
            &[change.to_string()],
        )
        .expect_err("forged tx key should fail validation");
        assert!(
            err.to_string()
                .contains("primary tx key does not match transaction extra"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn validate_standard_outbound_transaction_accepts_fee_below_expected_cap() {
        let recipient = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(11),
            point_from_scalar(13),
        );
        let change = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(17),
            point_from_scalar(19),
        );
        let primary = Scalar::from(7u64);
        let fee = 42u64;
        let tx_hex = synthetic_pruned_tx_hex(
            fee,
            serialize_extra(&primary * ED25519_BASEPOINT_TABLE, vec![]),
            vec![
                synthetic_output_for_address(&primary, 0, &recipient, 1_000_000),
                synthetic_output_for_address(&primary, 1, &change, 2_000_000),
            ],
        );

        let validated = validate_outbound_transaction(
            &tx_hex,
            &[scalar_hex(7)],
            &recipient.to_string(),
            1_000_000,
            Some(fee + 8),
            &[change.to_string()],
        )
        .expect("lower actual fee should be accepted under the provided cap");
        assert!(validated.tracked_owned_output);
        assert_eq!(validated.actual_fee, fee);
    }

    #[test]
    fn validate_standard_outbound_transaction_rejects_fee_above_expected_cap() {
        let recipient = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(11),
            point_from_scalar(13),
        );
        let change = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(17),
            point_from_scalar(19),
        );
        let primary = Scalar::from(7u64);
        let fee = 42u64;
        let tx_hex = synthetic_pruned_tx_hex(
            fee,
            serialize_extra(&primary * ED25519_BASEPOINT_TABLE, vec![]),
            vec![
                synthetic_output_for_address(&primary, 0, &recipient, 1_000_000),
                synthetic_output_for_address(&primary, 1, &change, 2_000_000),
            ],
        );

        let err = validate_outbound_transaction(
            &tx_hex,
            &[scalar_hex(7)],
            &recipient.to_string(),
            1_000_000,
            Some(fee - 1),
            &[change.to_string()],
        )
        .expect_err("higher actual fee should still fail");
        assert!(
            err.to_string()
                .contains("fee mismatch: expected at most 41 for 1 inputs (41 per input), got 42"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn validate_standard_outbound_transaction_allows_fee_cap_per_actual_input() {
        let recipient = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(11),
            point_from_scalar(13),
        );
        let change = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(17),
            point_from_scalar(19),
        );
        let primary = Scalar::from(7u64);
        let expected_fee_per_input = 240_000_000u64;
        let actual_fee = 1_339_680_000u64;
        let tx_hex = synthetic_pruned_tx_hex_with_inputs(
            actual_fee,
            serialize_extra(&primary * ED25519_BASEPOINT_TABLE, vec![]),
            vec![
                synthetic_output_for_address(&primary, 0, &recipient, 1_000_000),
                synthetic_output_for_address(&primary, 1, &change, 2_000_000),
            ],
            15,
        );

        let validated = validate_outbound_transaction(
            &tx_hex,
            &[scalar_hex(7)],
            &recipient.to_string(),
            1_000_000,
            Some(expected_fee_per_input),
            &[change.to_string()],
        )
        .expect("multi-input outbound should be allowed up to the per-input fee cap");
        assert!(validated.tracked_owned_output);
        assert_eq!(validated.actual_fee, actual_fee);
    }

    #[test]
    fn validate_subaddress_outbound_with_additional_keys_succeeds() {
        let recipient = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Subaddress,
            point_from_scalar(23),
            point_from_scalar(29),
        );
        let change = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(31),
            point_from_scalar(37),
        );
        let primary = Scalar::from(5u64);
        let additional_recipient = Scalar::from(6u64);
        let additional_change = Scalar::from(7u64);
        let fee = 77u64;
        let tx_hex = synthetic_pruned_tx_hex(
            fee,
            serialize_extra(
                &primary * ED25519_BASEPOINT_TABLE,
                vec![
                    additional_recipient * recipient.spend(),
                    &additional_change * ED25519_BASEPOINT_TABLE,
                ],
            ),
            vec![
                synthetic_output_for_address(&additional_recipient, 0, &recipient, 9_000_000),
                synthetic_output_for_address(&additional_change, 1, &change, 5_000_000),
            ],
        );

        let validated = validate_outbound_transaction(
            &tx_hex,
            &[scalar_hex(5), scalar_hex(6), scalar_hex(7)],
            &recipient.to_string(),
            9_000_000,
            Some(fee),
            &[change.to_string()],
        )
        .expect("subaddress outbound validation should succeed");
        assert!(validated.tracked_owned_output);
    }

    #[test]
    fn validate_outbound_without_owned_vault_output_is_still_valid_but_untracked() {
        let recipient = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(11),
            point_from_scalar(13),
        );
        let unrelated = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(17),
            point_from_scalar(19),
        );
        let claimed_owned = MoneroAddress::new(
            Network::Mainnet,
            AddressType::Legacy,
            point_from_scalar(23),
            point_from_scalar(29),
        );
        let primary = Scalar::from(7u64);
        let fee = 42u64;
        let tx_hex = synthetic_pruned_tx_hex(
            fee,
            serialize_extra(&primary * ED25519_BASEPOINT_TABLE, vec![]),
            vec![
                synthetic_output_for_address(&primary, 0, &recipient, 1_000_000),
                synthetic_output_for_address(&primary, 1, &unrelated, 2_000_000),
            ],
        );

        let validated = validate_outbound_transaction(
            &tx_hex,
            &[scalar_hex(7)],
            &recipient.to_string(),
            1_000_000,
            Some(fee),
            &[claimed_owned.to_string()],
        )
        .expect("validation should succeed even without a vault-owned output");
        assert!(!validated.tracked_owned_output);
    }
}
