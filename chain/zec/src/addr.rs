use crate::error::ZecError;
use tracing::{debug, info, warn}; // Import tracing macros
#[cfg(feature = "uniffi")]
use uniffi::{export, Enum};
use zcash_keys::address::Address;
use zcash_primitives::consensus::Parameters;
use crate::network::Network;
use zcash_address::unified::Typecode;
use bitflags::bitflags;

bitflags! {
    /// A set of flags describing the components found in a Unified Address.
    #[derive(Clone, Copy, Debug, PartialEq, Eq)]
    pub struct ComponentFlags: u8 {
        const EMPTY       = 0b0000_0000;
        const TRANSPARENT = 0b0000_0001;
        const SAPLING     = 0b0000_0010;
        const ORCHARD     = 0b0000_0100;
    }
}

/// Helper function
///
/// Inspects a Unified Address and returns a set of flags indicating which
/// component types it contains.
pub fn get_ua_components(ua: &zcash_keys::address::UnifiedAddress) -> ComponentFlags {
    let mut flags = ComponentFlags::EMPTY;

    for typecode in ua.receiver_types() {
        match typecode {
            Typecode::P2pkh | Typecode::P2sh => {
                flags |= ComponentFlags::TRANSPARENT;
            },
            Typecode::Sapling => {
                flags |= ComponentFlags::SAPLING;
            },
            Typecode::Orchard => {
                flags |= ComponentFlags::ORCHARD;
            },
            _ => (), // Ignore unknown types
        }
    }
    flags
}

pub fn validate_address_v124(address: String, network: Network) -> Result<(), ZecError> {
    let r: Option<Address> = Address::decode(&network, &address);
    // Match on the result of decode
    match r {
        Some(Address::Tex(_)) => {
            warn!(addr = %address, "Decoded as TEX address (considered invalid)");
            // treat TEX address as invalid
            Err(ZecError::GenericError(format!("Invalid address type (TEX): {address}")))
        }
        Some(Address::Transparent(_transparent_addr)) => {
            // transparent address types
            debug!(addr = %address, kind = "Transparent", "Decoded as a valid transparent address");
            Ok(())
        }
        Some(Address::Sapling(_)) => {
            // We now support Sapling addresses for receiving
            debug!(addr = %address, kind = "Sapling", "Decoded as a valid Sapling address");
            Ok(())
        }
        Some(Address::Unified(unified_addr)) => {
            let components = get_ua_components(&unified_addr);

            // For unified addresses, we need at least one of transparent, sapling, or orchard component
            if components.intersects(ComponentFlags::TRANSPARENT | ComponentFlags::SAPLING | ComponentFlags::ORCHARD) {
                Ok(())
            } else {
                Err(ZecError::GenericError(format!("Unified address without transparent, sapling, or orchard components: {address}")))
            }
        }
        None => {
            // decoding failed entirely (bad format, checksum, etc.)
            Err(ZecError::GenericError(format!("Invalid address format or checksum for {network}: {address}")))
        }
    }
}

pub fn validate_address_v1(address: String, network: Network) -> Result<(), ZecError> { // zec-shielded
    let r: Option<Address> = Address::decode(&network, &address);
    // Match on the result of decode
    match r {
        Some(Address::Tex(_)) => {
            warn!(addr = %address, "Decoded as TEX address (considered invalid)");
            // treat TEX address as invalid
            Err(ZecError::GenericError(format!("Invalid address type (TEX): {}", address)))
        }
        Some(Address::Transparent(_transparent_addr)) => {
            // transparent address types
            debug!(addr = %address, kind = "Transparent", "Decoded as a valid transparent address");
            Ok(())
        }
        Some(_) => {
            debug!(addr = %address, "Decoded as valid but unsupported address");
            Err(ZecError::GenericError(format!("Unsupported address type: {}", address)))
        }
        None => {
            // decoding failed entirely (bad format, checksum, etc.)
            info!(addr = %address, "Failed to decode address (invalid format/checksum)");
            Err(ZecError::GenericError(format!("Invalid address format or checksum: {}", address)))
        }
    }
}

// Helper function to determine if an address is transparent
pub fn is_transparent_address(address: &str, network: &Network) -> bool {
    if let Some(addr) = Address::decode(network, address) {
        match addr {
            Address::Transparent(_) => true,
            Address::Unified(ua) => ua.transparent().is_some(),
            _ => false,
        }
    } else {
        false
    }
}

// Helper function to validate that we can receive transactions from shielded addresses
pub fn validate_shielded_source(address: &str, network: &Network) -> Result<(), ZecError> {
    if let Some(addr) = Address::decode(network, address) {
        match addr {
            // Any shielded address can send to our transparent addresses
            Address::Sapling(_) => Ok(()),
            Address::Unified(unified_addr) => {
                let components = get_ua_components(&unified_addr);

                // Unified addresses must have either Sapling or Orchard components
                if components.intersects(ComponentFlags::SAPLING | ComponentFlags::ORCHARD) {
                    Ok(())
                } else {
                    Err(ZecError::GenericError(format!(
                        "Unified address without Sapling or Orchard component: {address}"
                    )))
                }
            },
            _ => Err(ZecError::GenericError(format!(
                "Address is not a supported shielded type: {address}"
            ))),
        }
    } else {
        Err(ZecError::InvalidAddress(address.to_string()))
    }
}
