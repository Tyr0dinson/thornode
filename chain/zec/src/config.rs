use once_cell::sync::OnceCell;
use tracing::{info, warn};
use tracing_subscriber::{fmt, EnvFilter, layer::SubscriberExt, util::SubscriberInitExt, registry}; 

use crate::network::Network;
use crate::error::ZecError;
#[cfg(feature = "shielded")]
use zcash_proofs::{download_sapling_parameters, prover::LocalTxProver};
#[cfg(feature = "shielded")]
use orchard::circuit::ProvingKey;

// Lazy static provers - loaded only when first accessed
#[cfg(feature = "shielded")]
static SAPLING_PROVER: OnceCell<LocalTxProver> = OnceCell::new();
#[cfg(feature = "shielded")]
static ORCHARD_PROVER: OnceCell<ProvingKey> = OnceCell::new();

pub fn init_zec() -> Result<(), ZecError> {
    init_logger();
    Ok(())
}

#[cfg(feature = "shielded")]
pub fn sapling_prover() -> Result<&'static LocalTxProver, ZecError> {
    let prover_ref = SAPLING_PROVER.get_or_try_init(|| {
        info!("Loading Sapling prover...");
        let p = download_sapling_parameters(None)
            .map_err(|e| ZecError::InitError(format!(
                "Failed to download Sapling params: {e}"
            )))?;
        let prover = LocalTxProver::new(&p.spend, &p.output);
        info!("Sapling prover loaded successfully.");
        Ok::<LocalTxProver, ZecError>(prover)
    })?;
    Ok(prover_ref)
}

#[cfg(feature = "shielded")]
pub fn orchard_prover() -> Result<&'static ProvingKey, ZecError> {
    let p = ORCHARD_PROVER.get_or_init(|| {
        info!("Loading Orchard prover...");
        let prover = ProvingKey::build();
        info!("Orchard prover loaded successfully.");
        prover
    });
    Ok(p)
}

// Optional: Load both provers at once if needed
#[cfg(feature = "shielded")]
pub fn preload_provers() -> Result<(), ZecError> {
    sapling_prover()?;
    orchard_prover()?;
    Ok(())
}

pub fn init_logger(){
    // Reads filter configuration from RUST_LOG environment variable
    // Example RUST_LOG values:
    //   RUST_LOG=info                (Show info, warn, error)
    //   RUST_LOG=debug               (Show debug, info, warn, error)
    //   RUST_LOG=trace               (Show all levels)
    //   RUST_LOG=warn,my_crate=debug (Show warn+ globally, but debug+ for your crate)
    //   RUST_LOG=error               (Show only errors)
    let filter = EnvFilter::try_from_default_env()
        .unwrap_or_else(|_| EnvFilter::new("info")); // Default to "info" if RUST_LOG not set

    // Customize the fmt::Layer
    let format_layer = fmt::layer()
    .with_level(true)     // Show log level
    .with_target(true)    // Show module path
    .with_thread_ids(true) // Show thread ID
    .with_timer(fmt::time::UtcTime::rfc_3339());
    // .with_file(true)
    // .with_line_number(true)

    match tracing_subscriber::registry()
        .with(format_layer)
        .with(filter)
        .try_init()
    {
        Ok(()) => info!("Logger initialized successfully."),
        Err(e) => {
            // Log a warning if it's already initialized, or potentially error on other issues.
            warn!("Logger initialization failed or logger already initialized: {}", e);
        }
    }
}
