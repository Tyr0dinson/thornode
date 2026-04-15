use anyhow::{Context, Result};
use curve25519_dalek::{constants::ED25519_BASEPOINT_TABLE, EdwardsPoint, Scalar};
use monero_wallet::{
    address::{AddressType, MoneroAddress, Network},
    extra::{Extra, ExtraField},
    io::write_varint,
    primitives::{keccak256, keccak256_to_scalar, Commitment},
    ringct::{EncryptedAmount, PrunedRctProofs, RctBase, RctType},
    transaction::{Input, Output, Pruned, Timelock, Transaction, TransactionPrefix},
};
use zeroize::Zeroizing;

#[derive(Clone)]
struct OutputDerivations {
    view_tag: u8,
    shared_key: Scalar,
}

pub fn validate_outbound_transaction(
    tx_hex: &str,
    tx_keys: &[String],
    to_address: &str,
    expected_amount: u64,
    expected_fee: Option<u64>,
    owned_addresses: &[String],
) -> Result<()> {
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
        if fee != expected_fee {
            anyhow::bail!("fee mismatch: expected {expected_fee}, got {fee}");
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

    let mut matched_destination = false;
    let mut matched_owned = owned_addresses.is_empty();
    for (index, output) in prefix.outputs.iter().enumerate() {
        let tx_key_candidates = tx_key_candidates_for_output(
            &address,
            &parsed_tx_keys,
            additional_keys.as_deref(),
            index,
        )?;
        if tx_key_candidates.iter().any(|tx_key| {
            matches_output(
                &prefix.inputs,
                output,
                &proofs.base.encrypted_amounts[index],
                &proofs.base.commitments[index],
                &address,
                tx_key,
                index,
                Some(expected_amount),
            )
            .unwrap_or(false)
        }) {
            matched_destination = true;
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

    if !matched_destination {
        anyhow::bail!(
            "no transaction output matched the provided tx key(s), destination, and amount"
        );
    }
    if !matched_owned {
        anyhow::bail!("no transaction output matched a registered vault address");
    }
    Ok(())
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
        return Ok(false);
    }

    let expected_output_key =
        (address.spend() + (&derivations.shared_key * ED25519_BASEPOINT_TABLE)).compress();
    if output.key != expected_output_key {
        return Ok(false);
    }

    let commitment = decrypt_commitment(&derivations, encrypted_amount);
    if let Some(expected_amount) = expected_amount {
        if commitment.amount != expected_amount {
            return Ok(false);
        }
    }
    Ok(commitment.calculate() == *commitment_point)
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
mod tests {
    use super::*;

    fn point_from_scalar(value: u64) -> EdwardsPoint {
        &Scalar::from(value) * ED25519_BASEPOINT_TABLE
    }

    fn scalar_hex(value: u64) -> String {
        hex::encode(Scalar::from(value).to_bytes())
    }

    struct SyntheticOutput {
        output: Output,
        encrypted_amount: EncryptedAmount,
        commitment: EdwardsPoint,
    }

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
                key: (address.spend() + (&derivations.shared_key * ED25519_BASEPOINT_TABLE))
                    .compress(),
                view_tag: Some(derivations.view_tag),
            },
            encrypted_amount: EncryptedAmount::Compact {
                amount: (amount ^ u64::from_le_bytes(amount_mask_8)).to_le_bytes(),
            },
            commitment,
        }
    }

    fn serialize_extra(primary: EdwardsPoint, additional: Vec<EdwardsPoint>) -> Vec<u8> {
        let mut extra = ExtraField::PublicKey(primary).serialize();
        if !additional.is_empty() {
            extra.extend(ExtraField::PublicKeys(additional).serialize());
        }
        extra
    }

    fn synthetic_pruned_tx_hex(fee: u64, extra: Vec<u8>, outputs: Vec<SyntheticOutput>) -> String {
        let tx = Transaction::<Pruned>::V2 {
            prefix: TransactionPrefix {
                additional_timelock: Timelock::None,
                inputs: vec![Input::ToKey {
                    amount: None,
                    key_offsets: vec![1],
                    key_image: point_from_scalar(99),
                }],
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

        validate_outbound_transaction(
            &tx_hex,
            &[scalar_hex(7)],
            &recipient.to_string(),
            1_000_000,
            Some(fee),
            &[change.to_string()],
        )
        .expect("standard outbound validation should succeed");
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

        validate_outbound_transaction(
            &tx_hex,
            &[scalar_hex(5), scalar_hex(6), scalar_hex(7)],
            &recipient.to_string(),
            9_000_000,
            Some(fee),
            &[change.to_string()],
        )
        .expect("subaddress outbound validation should succeed");
    }

    #[test]
    fn validate_outbound_requires_owned_vault_output() {
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

        let err = validate_outbound_transaction(
            &tx_hex,
            &[scalar_hex(7)],
            &recipient.to_string(),
            1_000_000,
            Some(fee),
            &[claimed_owned.to_string()],
        )
        .expect_err("validation should reject tx without a vault-owned output");
        assert!(err
            .to_string()
            .contains("no transaction output matched a registered vault address"));
    }
}
