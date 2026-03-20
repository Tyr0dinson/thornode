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
use frost::sign::SignCoordinator;
use state::store::IDEMPOTENCY_PRUNE_INTERVAL_SECS;
use state::Store;
use tx::daemon::DaemonClient;
use tx::scanner::Scanner;

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
        "configuration loaded"
    );
    let auth_config = AuthConfig {
        username: config.auth_username.clone(),
        password: config.auth_password.clone(),
        bearer: config.auth_bearer.clone(),
    };
    if !auth_config.is_enabled() {
        anyhow::bail!(
            "xmr-frost-signer requires configured auth (XMR_FROST_AUTH_BEARER or basic auth)"
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
    let store = Arc::new(Store::open(&config.data_dir, &config.store_secret)?);
    let daemon = DaemonClient::new(&config.daemon_url, config.daemon_max_response_bytes);

    // Initialize scanner and load registered vaults
    let mut scanner = Scanner::new();
    let vaults = store.list_vaults()?;
    for vault in &vaults {
        let outputs = store.load_outputs(&vault.vault_pub_key)?;
        let height = store.load_scan_height(&vault.vault_pub_key)?;
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

    let app_state = Arc::new(AppState {
        dkg,
        signer,
        scanner: parking_lot::Mutex::new(scanner),
        daemon: daemon.clone(),
        store,
        chain_network: config.chain_network,
        max_sign_scan_lag_blocks: config.max_sign_scan_lag_blocks,
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
        .route("/v1/monero/broadcast", post(api::routes::broadcast))
        // Scanner / Balance
        .route(
            "/v1/monero/balance/{vault_pub_key}",
            get(api::routes::get_balance),
        )
        .route("/v1/monero/transfers", get(api::routes::get_transfers))
        .route(
            "/v1/monero/addresses/{vault_pub_key}",
            get(api::routes::get_addresses),
        )
        .route(
            "/v1/monero/outputs/{vault_pub_key}",
            get(api::routes::get_outputs),
        )
        .route("/v1/monero/scanner/sync", post(api::routes::scanner_sync))
        .route("/v1/monero/fee-rate", get(api::routes::get_fee_rate))
        .route(
            "/v1/monero/chain/height",
            get(api::routes::get_chain_height),
        )
        .route(
            "/v1/monero/chain/block/{height}",
            get(api::routes::get_chain_block),
        )
        .route(
            "/v1/monero/chain/tx/{tx_hash}/exists",
            get(api::routes::get_chain_tx_exists),
        )
        .layer(from_fn(api::rate_limit::rate_limit_middleware))
        .layer(Extension(rate_limiter))
        .layer(from_fn(api::auth::auth_middleware))
        .layer(Extension(auth_config));

    // Build router.
    let app = Router::new()
        // Health remains unauthenticated for probes.
        .route("/health", get(health::health_check))
        .merge(monero_routes)
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
