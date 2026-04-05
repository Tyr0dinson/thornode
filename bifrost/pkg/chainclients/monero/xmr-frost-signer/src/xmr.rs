use anyhow::{Context, Result};
use curve25519_dalek::{
    constants::ED25519_BASEPOINT_TABLE,
    edwards::{CompressedEdwardsY, EdwardsPoint},
    scalar::clamp_integer,
    Scalar,
};
use monero_wallet::{
    address::{AddressType, MoneroAddress, Network},
    GuaranteedViewPair, ViewPair,
};
use sha2::{Digest, Sha512};
use zeroize::Zeroizing;

use crate::config::ChainNetworkMode;

const VIEW_KEY_DOMAIN: &[u8] = b"THORChain View Key";

pub fn additional_view_scalar() -> Scalar {
    let mut hasher = Sha512::new();
    hasher.update(VIEW_KEY_DOMAIN);
    hasher.update(0u64.to_le_bytes());
    let digest: [u8; 64] = hasher.finalize().into();

    Scalar::from_bytes_mod_order_wide(&digest)
}

pub fn spend_point_from_group_key_hex(group_key_hex: &str) -> Result<EdwardsPoint> {
    let bytes = hex::decode(group_key_hex).with_context(|| {
        format!("invalid group key hex for address derivation: {group_key_hex}")
    })?;
    let compressed: [u8; 32] = bytes
        .as_slice()
        .try_into()
        .map_err(|_| anyhow::anyhow!("group key must be 32 bytes"))?;

    CompressedEdwardsY(compressed)
        .decompress()
        .ok_or_else(|| anyhow::anyhow!("group key is not a valid Ed25519 point"))
}

pub fn spend_scalar_from_seed_hex(spend_key_hex: &str) -> Result<Zeroizing<Scalar>> {
    let bytes = hex::decode(spend_key_hex).with_context(|| {
        format!("invalid spend key hex for address derivation: {spend_key_hex}")
    })?;
    let spend_seed: [u8; 32] = bytes
        .as_slice()
        .try_into()
        .map_err(|_| anyhow::anyhow!("spend key seed must be 32 bytes"))?;

    let digest: [u8; 64] = Sha512::digest(spend_seed).into();
    let scalar_bytes: [u8; 32] =
        clamp_integer(digest[..32].try_into().expect("digest slice length"));
    Ok(Zeroizing::new(Scalar::from_bytes_mod_order(scalar_bytes)))
}

pub fn group_key_hex_from_spend_seed_hex(spend_key_hex: &str) -> Result<String> {
    let spend_scalar = spend_scalar_from_seed_hex(spend_key_hex)?;
    let spend_point = &*spend_scalar * ED25519_BASEPOINT_TABLE;
    Ok(hex::encode(spend_point.compress().to_bytes()))
}

pub fn derive_address_from_spend_seed_hex(
    spend_key_hex: &str,
    chain_network: ChainNetworkMode,
) -> Result<String> {
    let group_key_hex = group_key_hex_from_spend_seed_hex(spend_key_hex)?;
    derive_vault_address(&group_key_hex, chain_network)
}

pub fn view_pair_from_group_key_hex(group_key_hex: &str) -> Result<ViewPair> {
    let spend = spend_point_from_group_key_hex(group_key_hex)?;
    let view = Zeroizing::new(additional_view_scalar());
    ViewPair::new(spend, view).map_err(|e| anyhow::anyhow!("failed to construct view pair: {}", e))
}

pub fn guaranteed_view_pair_from_group_key_hex(group_key_hex: &str) -> Result<GuaranteedViewPair> {
    let spend = spend_point_from_group_key_hex(group_key_hex)?;
    let view = Zeroizing::new(additional_view_scalar());
    GuaranteedViewPair::new(spend, view)
        .map_err(|e| anyhow::anyhow!("failed to construct guaranteed view pair: {}", e))
}

pub fn derive_vault_address(
    group_key_hex: &str,
    chain_network: ChainNetworkMode,
) -> Result<String> {
    match chain_network {
        ChainNetworkMode::Mocknet => Ok(view_pair_from_group_key_hex(group_key_hex)?
            .legacy_address(Network::Mainnet)
            .to_string()),
        ChainNetworkMode::Public => {
            let spend = spend_point_from_group_key_hex(group_key_hex)?;
            let view_scalar = additional_view_scalar();
            let view = &view_scalar * ED25519_BASEPOINT_TABLE;
            Ok(MoneroAddress::new(
                Network::Mainnet,
                AddressType::Featured {
                    subaddress: true,
                    payment_id: None,
                    guaranteed: true,
                },
                spend,
                view,
            )
            .to_string())
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const CROSS_STACK_GROUP_KEY_HEX: &str =
        "b862409fb5c4c4123df2abf7462b88f041ad36dd6864ce872fd5472be363c5b1";
    const CROSS_STACK_MOCK_ADDRESS: &str =
        "48cNbRvGyrb43yGQYLWKxshBnnq8nRkFfPcUwfqpihwJWiSwHb7gy6p6oAXAxDJXbPbGME2xWBgFdSUzrxhiKLiqGbm9KqB";
    const CROSS_STACK_PUBLIC_ADDRESS: &str =
        "Cq5MKQVvbmV43yGQYLWKxshBnnq8nRkFfPcUwfqpihwJWiSwHb7gy6p6oAXAxDJXbPbGME2xWBgFdSUzrxhiKLiq2BjPq9hME";

    #[test]
    fn derive_address_uses_expected_mode_prefix() {
        let base = Scalar::from(7u64);
        let group_key_hex = hex::encode((&base * ED25519_BASEPOINT_TABLE).compress().to_bytes());
        let mocknet = derive_vault_address(&group_key_hex, ChainNetworkMode::Mocknet).unwrap();
        let public = derive_vault_address(&group_key_hex, ChainNetworkMode::Public).unwrap();

        assert!(mocknet.starts_with('4'));
        assert!(public.starts_with('C'));
        assert_eq!(mocknet.len(), 95);
        assert_eq!(public.len(), 97);
        assert_ne!(mocknet, public);

        let mocknet_parsed = MoneroAddress::from_str_with_unchecked_network(&mocknet).unwrap();
        let public_parsed = MoneroAddress::from_str_with_unchecked_network(&public).unwrap();
        assert!(!mocknet_parsed.is_guaranteed());
        assert!(public_parsed.is_guaranteed());
        assert!(!mocknet_parsed.is_subaddress());
        assert!(public_parsed.is_subaddress());
    }

    #[test]
    fn derive_address_matches_cross_stack_fixture() {
        let mocknet =
            derive_vault_address(CROSS_STACK_GROUP_KEY_HEX, ChainNetworkMode::Mocknet).unwrap();
        let public =
            derive_vault_address(CROSS_STACK_GROUP_KEY_HEX, ChainNetworkMode::Public).unwrap();

        assert_eq!(mocknet, CROSS_STACK_MOCK_ADDRESS);
        assert_eq!(public, CROSS_STACK_PUBLIC_ADDRESS);
    }

    #[test]
    fn invalid_group_key_rejected() {
        let err = derive_vault_address("deadbeef", ChainNetworkMode::Mocknet).unwrap_err();
        assert!(err.to_string().contains("group key"));
    }
}
