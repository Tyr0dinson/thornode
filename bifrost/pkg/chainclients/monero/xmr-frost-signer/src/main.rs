mod api;
mod config;
mod frost;
mod health;
mod state;
mod tx;
mod xmr;

use std::sync::Arc;

use axum::{
    middleware::from_fn,
    routing::{get, post},
    Extension, Router,
};
use tower_http::trace::TraceLayer;
use tracing::{info, warn};
use tracing_subscriber::{fmt, prelude::*, EnvFilter};

use api::auth::AuthConfig;
use api::rate_limit::{RateLimitConfig, RateLimiter};
use api::routes::AppState;
use config::{redact_url_userinfo, Config};
use frost::dkg::DkgCoordinator;
use frost::key_image::KeyImageCoordinator;
use frost::sign::SignCoordinator;
use state::store::IDEMPOTENCY_PRUNE_INTERVAL_SECS;
use state::Store;
use tx::daemon::DaemonClient;
use tx::scanner::Scanner;

const BALANCE_ROUTE: &str = "/v1/monero/balance/:vault_pub_key";
const ADDRESSES_ROUTE: &str = "/v1/monero/addresses/:vault_pub_key";
const OUTPUTS_ROUTE: &str = "/v1/monero/outputs/:vault_pub_key";
const REGISTER_VAULT_ROUTE: &str = "/v1/monero/vaults/register";
const CHAIN_BLOCK_ROUTE: &str = "/v1/monero/chain/block/:height";
const CHAIN_TX_EXISTS_ROUTE: &str = "/v1/monero/chain/tx/:tx_hash/exists";

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    // Initialize tracing
    tracing_subscriber::registry()
        .with(EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new("info")))
        .with(fmt::layer().json())
        .init();

    info!("xmr-frost-signer starting");

    // Load configuration
    let config = Config::from_env()?;
    let daemon_url = redact_url_userinfo(&config.daemon_url);
    info!(
        listen_addr = %config.listen_addr,
        daemon_url = %daemon_url,
        daemon_disable_tls = config.daemon_disable_tls,
        daemon_max_response_bytes = config.daemon_max_response_bytes,
        max_dkg_sessions = config.max_dkg_sessions,
        max_sign_sessions = config.max_sign_sessions,
        max_sign_scan_lag_blocks = config.max_sign_scan_lag_blocks,
        rate_limit_global_rps = config.rate_limit_global_rps,
        rate_limit_global_burst = config.rate_limit_global_burst,
        rate_limit_per_ip_rps = config.rate_limit_per_ip_rps,
        rate_limit_per_ip_burst = config.rate_limit_per_ip_burst,
        rate_limit_max_tracked_ips = config.rate_limit_max_tracked_ips,
        data_dir = %config.data_dir.display(),
        chain_network = ?config.chain_network,
        sim_helper_enabled = config.sim_helper_enabled,
        "configuration loaded"
    );
    let auth_config = AuthConfig {
        username: config.auth_username.clone(),
        password: config.auth_password.clone(),
        bearer: config.auth_bearer.clone(),
    };
    if !auth_config.is_enabled() {
        anyhow::bail!(
            "xmr-frost-signer requires configured auth (raw Authorization token via XMR_FROST_AUTH_BEARER or SIGNER_SEED_PHRASE, or basic auth)"
        );
    }
    info!("sidecar auth enabled for protected routes");
    let rate_limiter = RateLimiter::new(RateLimitConfig {
        global_rps: config.rate_limit_global_rps,
        global_burst: config.rate_limit_global_burst,
        per_ip_rps: config.rate_limit_per_ip_rps,
        per_ip_burst: config.rate_limit_per_ip_burst,
        max_tracked_ips: config.rate_limit_max_tracked_ips,
    });

    // Initialize components
    let store = Arc::new(Store::open(&config.data_dir)?);
    let daemon = DaemonClient::new(&config.daemon_url, config.daemon_max_response_bytes);

    // Initialize scanner and load registered vaults
    let mut scanner = Scanner::new();
    let vaults = store.list_vaults()?;
    for vault in &vaults {
        let outputs = store.load_outputs(&vault.vault_pub_key)?;
        let height = store
            .load_scan_height(&vault.vault_pub_key)?
            .or_else(|| (vault.birthday > 0).then_some(vault.birthday));
        if let Err(e) = scanner.register_vault(
            &vault.vault_pub_key,
            &vault.group_key,
            config.chain_network,
            outputs,
            height,
        ) {
            warn!(vault = %vault.vault_pub_key, error = %e, "failed to initialize scanner state");
        }
    }

    let dkg = DkgCoordinator::new(store.clone(), config.chain_network, config.max_dkg_sessions);
    let signer = SignCoordinator::new(
        store.clone(),
        daemon.clone(),
        config.chain_network,
        config.max_sign_sessions,
    );
    let key_images = KeyImageCoordinator::new(store.clone(), config.max_sign_sessions);

    let app_state = Arc::new(AppState {
        dkg,
        signer,
        key_images,
        scanner: parking_lot::Mutex::new(scanner),
        scan_lifecycle: tokio::sync::Mutex::new(()),
        daemon: daemon.clone(),
        store,
        chain_network: config.chain_network,
        max_sign_scan_lag_blocks: config.max_sign_scan_lag_blocks,
        sim_helper_enabled: config.sim_helper_enabled,
        sim_helper_wallet_rpc_url: config.sim_helper_wallet_rpc_url.clone(),
    });

    // Protected router for signer APIs.
    let monero_routes = Router::new()
        // DKG
        .route("/v1/monero/dkg/round1", post(api::routes::dkg_round1))
        .route("/v1/monero/dkg/round2", post(api::routes::dkg_round2))
        .route("/v1/monero/dkg/round3", post(api::routes::dkg_round3))
        .route("/v1/monero/dkg/finalize", post(api::routes::dkg_finalize))
        // Signing
        .route("/v1/monero/sign/prepare", post(api::routes::sign_prepare))
        .route("/v1/monero/sign/partial", post(api::routes::sign_partial))
        .route("/v1/monero/sign/combine", post(api::routes::sign_combine))
        .route(
            "/v1/monero/key-image/prepare",
            post(api::routes::key_image_prepare),
        )
        .route(
            "/v1/monero/key-image/partial",
            post(api::routes::key_image_partial),
        )
        .route(
            "/v1/monero/key-image/combine",
            post(api::routes::key_image_combine),
        )
        .route(
            "/v1/monero/key-image/validate",
            post(api::routes::key_image_validate),
        )
        .route(
            "/v1/monero/sessions/clear",
            post(api::routes::clear_sessions),
        )
        .route("/v1/monero/broadcast", post(api::routes::broadcast))
        // Scanner / Balance
        .route(REGISTER_VAULT_ROUTE, post(api::routes::register_vault))
        .route("/v1/monero/vaults", get(api::routes::list_vault_states))
        .route(BALANCE_ROUTE, get(api::routes::get_balance))
        .route("/v1/monero/transfers", get(api::routes::get_transfers))
        .route(
            "/v1/monero/outbounds/validate",
            post(api::routes::validate_outbound),
        )
        .route(
            "/v1/monero/outbounds/prune",
            post(api::routes::prune_outbounds),
        )
        .route(
            "/v1/monero/outbounds/:vault_pub_key",
            get(api::routes::get_outbounds),
        )
        .route(ADDRESSES_ROUTE, get(api::routes::get_addresses))
        .route(OUTPUTS_ROUTE, get(api::routes::get_outputs))
        .route(
            "/v1/monero/outputs/apply-key-images",
            post(api::routes::apply_key_images),
        )
        .route("/v1/monero/scanner/sync", post(api::routes::scanner_sync))
        .route("/v1/monero/scanner/reset", post(api::routes::scanner_reset))
        .route("/v1/monero/keys", get(api::routes::list_vault_keys))
        .route(
            "/v1/monero/keys/:vault_pub_key/export",
            get(api::routes::key_export),
        )
        .route("/v1/monero/keys/restore", post(api::routes::key_restore))
        .route("/v1/monero/fee-rate", get(api::routes::get_fee_rate))
        .route(
            "/v1/monero/chain/height",
            get(api::routes::get_chain_height),
        )
        .route(CHAIN_BLOCK_ROUTE, get(api::routes::get_chain_block))
        .route(CHAIN_TX_EXISTS_ROUTE, get(api::routes::get_chain_tx_exists))
        .layer(from_fn(api::rate_limit::rate_limit_middleware))
        .layer(Extension(rate_limiter.clone()))
        .layer(from_fn(api::auth::auth_middleware))
        .layer(Extension(auth_config.clone()));

    let sim_helper_routes = if config.sim_helper_enabled {
        Router::new()
            .route("/v1/monero/sim/balance", post(api::routes::sim_get_balance))
            .route("/v1/monero/sim/transfer", post(api::routes::sim_transfer))
            .route("/v1/monero/sim/faucet", post(api::routes::sim_faucet))
            .layer(from_fn(api::rate_limit::rate_limit_middleware))
            .layer(Extension(rate_limiter.clone()))
            .layer(from_fn(api::auth::auth_middleware))
            .layer(Extension(auth_config.clone()))
    } else {
        Router::new()
    };

    // Build router.
    let app = Router::new()
        // Health remains unauthenticated for probes.
        .route("/health", get(health::health_check))
        .merge(monero_routes)
        .merge(sim_helper_routes)
        // Middleware
        .layer(TraceLayer::new_for_http())
        .with_state(app_state.clone());

    // Start session cleanup task
    let cleanup_state = app_state.clone();
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(std::time::Duration::from_secs(60));
        loop {
            interval.tick().await;
            cleanup_state.dkg.cleanup_expired();
            cleanup_state.signer.cleanup_expired();
            cleanup_state.key_images.cleanup_expired();
        }
    });

    // Start idempotency pruning task
    let idempotency_prune_state = app_state.clone();
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(std::time::Duration::from_secs(
            IDEMPOTENCY_PRUNE_INTERVAL_SECS,
        ));
        loop {
            interval.tick().await;
            let now_ts = chrono::Utc::now().timestamp();
            match idempotency_prune_state
                .store
                .prune_expired_idempotency(now_ts)
            {
                Ok(0) => {}
                Ok(removed) => info!(removed, "pruned expired idempotency records"),
                Err(e) => warn!(error = %e, "failed to prune expired idempotency records"),
            }
        }
    });

    // Reservation reconciler: releases stale reservations and finalizes observed ones.
    let reconcile_state = app_state.clone();
    tokio::spawn(async move {
        let mut interval = tokio::time::interval(std::time::Duration::from_secs(60));
        loop {
            interval.tick().await;
            if let Err(e) = reconcile_state.signer.reconcile_reservations().await {
                warn!(error = %e, "failed to reconcile output reservations");
            }
        }
    });

    // Start server
    let listener = tokio::net::TcpListener::bind(config.listen_addr).await?;
    info!(addr = %config.listen_addr, "listening");

    axum::serve(
        listener,
        app.into_make_service_with_connect_info::<std::net::SocketAddr>(),
    )
    .with_graceful_shutdown(shutdown_signal())
    .await?;

    info!("xmr-frost-signer shut down");
    Ok(())
}

async fn shutdown_signal() {
    let ctrl_c = async {
        tokio::signal::ctrl_c()
            .await
            .expect("failed to install Ctrl+C handler");
    };

    #[cfg(unix)]
    let terminate = async {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .expect("failed to install SIGTERM handler")
            .recv()
            .await;
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => info!("received Ctrl+C, shutting down"),
        _ = terminate => info!("received SIGTERM, shutting down"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    use axum::{
        body::Body,
        http::{Request, StatusCode},
    };
    use tower::ServiceExt;

    #[tokio::test]
    async fn parameterized_routes_match_monero_paths() {
        let app = Router::new()
            .route(BALANCE_ROUTE, get(|| async { StatusCode::OK }))
            .route(ADDRESSES_ROUTE, get(|| async { StatusCode::OK }))
            .route(OUTPUTS_ROUTE, get(|| async { StatusCode::OK }))
            .route(CHAIN_BLOCK_ROUTE, get(|| async { StatusCode::OK }))
            .route(CHAIN_TX_EXISTS_ROUTE, get(|| async { StatusCode::OK }));

        for path in [
            "/v1/monero/balance/test-vault",
            "/v1/monero/addresses/test-vault",
            "/v1/monero/outputs/test-vault",
            "/v1/monero/chain/block/1",
            "/v1/monero/chain/tx/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/exists",
        ] {
            let response = app
                .clone()
                .oneshot(Request::builder().uri(path).body(Body::empty()).unwrap())
                .await
                .unwrap();
            assert_eq!(response.status(), StatusCode::OK, "route {path} should match");
        }
    }
}
