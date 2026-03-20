use std::net::{IpAddr, SocketAddr};
use std::path::PathBuf;

use base64::{
    engine::general_purpose::{STANDARD, STANDARD_NO_PAD, URL_SAFE, URL_SAFE_NO_PAD},
    Engine as _,
};
use tracing::warn;

const DEFAULT_DAEMON_MAX_RESPONSE_BYTES: usize = 50 * 1024 * 1024;
const DEFAULT_MAX_DKG_SESSIONS: usize = 128;
const DEFAULT_MAX_SIGN_SESSIONS: usize = 256;
const DEFAULT_MAX_SIGN_SCAN_LAG_BLOCKS: u64 = 3;
const DEFAULT_RATE_LIMIT_GLOBAL_RPS: f64 = 40.0;
const DEFAULT_RATE_LIMIT_GLOBAL_BURST: u32 = 80;
const DEFAULT_RATE_LIMIT_PER_IP_RPS: f64 = 10.0;
const DEFAULT_RATE_LIMIT_PER_IP_BURST: u32 = 20;
const DEFAULT_RATE_LIMIT_MAX_TRACKED_IPS: usize = 8192;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ChainNetworkMode {
    Mocknet,
    Public,
}

impl ChainNetworkMode {
    fn parse(value: &str) -> Result<Self, ConfigError> {
        match value.trim().to_ascii_lowercase().as_str() {
            "mocknet" => Ok(ChainNetworkMode::Mocknet),
            "public" => Ok(ChainNetworkMode::Public),
            other => Err(ConfigError::InvalidChainNetwork(other.to_string())),
        }
    }
}

/// Configuration for the XMR FROST signer sidecar.
/// All values are read from environment variables.
#[derive(Debug, Clone)]
pub struct Config {
    pub listen_addr: SocketAddr,
    pub daemon_url: String,
    pub daemon_disable_tls: bool,
    pub daemon_max_response_bytes: usize,
    pub max_dkg_sessions: usize,
    pub max_sign_sessions: usize,
    pub max_sign_scan_lag_blocks: u64,
    pub rate_limit_global_rps: f64,
    pub rate_limit_global_burst: u32,
    pub rate_limit_per_ip_rps: f64,
    pub rate_limit_per_ip_burst: u32,
    pub rate_limit_max_tracked_ips: usize,
    pub data_dir: PathBuf,
    pub auth_username: Option<String>,
    pub auth_password: Option<String>,
    pub auth_bearer: Option<String>,
    pub store_secret: String,
    pub chain_network: ChainNetworkMode,
}

impl Config {
    /// Load configuration from environment variables.
    pub fn from_env() -> Result<Self, ConfigError> {
        let listen_addr = std::env::var("XMR_FROST_LISTEN_ADDR")
            .unwrap_or_else(|_| "127.0.0.1:8283".to_string())
            .parse::<SocketAddr>()
            .map_err(|e| ConfigError::InvalidAddr(e.to_string()))?;

        let chain_network = ChainNetworkMode::parse(
            &std::env::var("XMR_FROST_CHAIN_NETWORK")
                .map_err(|_| ConfigError::Missing("XMR_FROST_CHAIN_NETWORK"))?,
        )?;

        let daemon_disable_tls = read_env_bool("XMR_FROST_DAEMON_DISABLE_TLS", false)?;
        let daemon_url_raw = std::env::var("XMR_FROST_DAEMON_URL")
            .map_err(|_| ConfigError::Missing("XMR_FROST_DAEMON_URL"))?;
        if daemon_url_raw.trim().is_empty() {
            return Err(ConfigError::Missing("XMR_FROST_DAEMON_URL"));
        }
        let daemon_url =
            normalize_and_validate_daemon_url(&daemon_url_raw, chain_network, daemon_disable_tls)?;

        let daemon_max_response_bytes = read_env_usize(
            "XMR_FROST_DAEMON_MAX_RESPONSE_BYTES",
            DEFAULT_DAEMON_MAX_RESPONSE_BYTES,
            1,
        )?;
        let max_dkg_sessions =
            read_env_usize("XMR_FROST_MAX_DKG_SESSIONS", DEFAULT_MAX_DKG_SESSIONS, 1)?;
        let max_sign_sessions =
            read_env_usize("XMR_FROST_MAX_SIGN_SESSIONS", DEFAULT_MAX_SIGN_SESSIONS, 1)?;
        let max_sign_scan_lag_blocks = read_env_u64(
            "XMR_FROST_MAX_SIGN_SCAN_LAG_BLOCKS",
            DEFAULT_MAX_SIGN_SCAN_LAG_BLOCKS,
            0,
        )?;
        let rate_limit_global_rps = read_env_f64(
            "XMR_FROST_RATE_LIMIT_GLOBAL_RPS",
            DEFAULT_RATE_LIMIT_GLOBAL_RPS,
            0.0,
        )?;
        let rate_limit_global_burst = read_env_u32(
            "XMR_FROST_RATE_LIMIT_GLOBAL_BURST",
            DEFAULT_RATE_LIMIT_GLOBAL_BURST,
            1,
        )?;
        let rate_limit_per_ip_rps = read_env_f64(
            "XMR_FROST_RATE_LIMIT_PER_IP_RPS",
            DEFAULT_RATE_LIMIT_PER_IP_RPS,
            0.0,
        )?;
        let rate_limit_per_ip_burst = read_env_u32(
            "XMR_FROST_RATE_LIMIT_PER_IP_BURST",
            DEFAULT_RATE_LIMIT_PER_IP_BURST,
            1,
        )?;
        let rate_limit_max_tracked_ips = read_env_usize(
            "XMR_FROST_RATE_LIMIT_MAX_TRACKED_IPS",
            DEFAULT_RATE_LIMIT_MAX_TRACKED_IPS,
            1,
        )?;

        let data_dir = std::env::var("XMR_FROST_DATA_DIR")
            .unwrap_or_else(|_| "/var/lib/xmr-frost".to_string())
            .into();

        let auth_username = read_non_empty_env("XMR_FROST_AUTH_USERNAME");
        let auth_password = read_non_empty_env("XMR_FROST_AUTH_PASSWORD");
        let auth_bearer = read_non_empty_env("XMR_FROST_AUTH_BEARER");

        if auth_username.is_some() != auth_password.is_some() {
            return Err(ConfigError::InvalidAuthConfig(
                "basic auth requires both XMR_FROST_AUTH_USERNAME and XMR_FROST_AUTH_PASSWORD",
            ));
        }
        let auth_enabled = auth_bearer.is_some() || auth_username.is_some();
        if !auth_enabled {
            return Err(ConfigError::MissingAuthConfig);
        }

        let store_secret = std::env::var("XMR_FROST_STORE_SECRET")
            .map_err(|_| ConfigError::Missing("XMR_FROST_STORE_SECRET"))?;
        if store_secret.trim().is_empty() {
            return Err(ConfigError::Missing("XMR_FROST_STORE_SECRET"));
        }
        let strong_store_secret = is_strong_store_secret(&store_secret);
        if chain_network == ChainNetworkMode::Public && !strong_store_secret {
            return Err(ConfigError::InvalidStoreSecretPublic);
        }
        if chain_network == ChainNetworkMode::Mocknet && !strong_store_secret {
            warn!(
                "XMR_FROST_STORE_SECRET is weak; this is permitted in mocknet only and must not be used in public mode"
            );
        }

        Ok(Config {
            listen_addr,
            daemon_url,
            daemon_disable_tls,
            daemon_max_response_bytes,
            max_dkg_sessions,
            max_sign_sessions,
            max_sign_scan_lag_blocks,
            rate_limit_global_rps,
            rate_limit_global_burst,
            rate_limit_per_ip_rps,
            rate_limit_per_ip_burst,
            rate_limit_max_tracked_ips,
            data_dir,
            auth_username,
            auth_password,
            auth_bearer,
            store_secret,
            chain_network,
        })
    }
}

fn normalize_and_validate_daemon_url(
    input: &str,
    chain_network: ChainNetworkMode,
    daemon_disable_tls: bool,
) -> Result<String, ConfigError> {
    let trimmed = input.trim();
    if trimmed.is_empty() {
        return Err(ConfigError::Missing("XMR_FROST_DAEMON_URL"));
    }

    let candidate = if trimmed.contains("://") {
        trimmed.to_string()
    } else {
        let scheme = if should_default_to_http(trimmed) {
            "http"
        } else {
            "https"
        };
        format!("{scheme}://{trimmed}")
    };

    let mut parsed = reqwest::Url::parse(&candidate)
        .map_err(|e| ConfigError::InvalidDaemonEndpoint(format!("{candidate:?}: {e}")))?;

    match parsed.scheme() {
        "http" | "https" => {}
        other => {
            return Err(ConfigError::InvalidDaemonEndpoint(format!(
                "unsupported scheme {other:?} (expected http|https)"
            )))
        }
    }

    let host = parsed
        .host_str()
        .ok_or_else(|| ConfigError::InvalidDaemonEndpoint("missing host".to_string()))?;
    if host.trim().is_empty() {
        return Err(ConfigError::InvalidDaemonEndpoint(
            "missing host".to_string(),
        ));
    }
    let host = host.to_string();

    normalize_daemon_path(&mut parsed);
    parsed.set_query(None);
    parsed.set_fragment(None);

    if chain_network == ChainNetworkMode::Public && !is_loopback_host(&host) {
        if parsed.scheme() == "http" && !daemon_disable_tls {
            return Err(ConfigError::InsecureDaemonEndpoint(format!(
                "non-loopback daemon endpoint {} must use https unless XMR_FROST_DAEMON_DISABLE_TLS=true",
                endpoint_for_message(&parsed)
            )));
        }
        if !daemon_disable_tls && parsed.username().trim().is_empty() {
            return Err(ConfigError::InsecureDaemonEndpoint(format!(
                "non-loopback daemon endpoint {} requires URL userinfo credentials when TLS is enforced",
                endpoint_for_message(&parsed)
            )));
        }
    }

    Ok(parsed.to_string().trim_end_matches('/').to_string())
}

fn normalize_daemon_path(parsed: &mut reqwest::Url) {
    let mut path = parsed.path().trim_end_matches('/').to_string();
    if path.ends_with("/json_rpc") {
        let new_len = path.len().saturating_sub("/json_rpc".len());
        path.truncate(new_len);
    }

    if path.is_empty() {
        parsed.set_path("");
    } else {
        parsed.set_path(&path);
    }
}

fn endpoint_for_message(parsed: &reqwest::Url) -> String {
    let mut safe = parsed.clone();
    if !safe.username().trim().is_empty() {
        let _ = safe.set_username("REDACTED");
    }
    if safe.password().is_some() {
        let _ = safe.set_password(Some("REDACTED"));
    }
    safe.to_string().trim_end_matches('/').to_string()
}

pub fn redact_url_userinfo(raw: &str) -> String {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return String::new();
    }
    match reqwest::Url::parse(trimmed) {
        Ok(parsed) => endpoint_for_message(&parsed),
        Err(_) => trimmed.to_string(),
    }
}

fn should_default_to_http(raw: &str) -> bool {
    let probe = format!("http://{raw}");
    if let Ok(url) = reqwest::Url::parse(&probe) {
        if let Some(host) = url.host_str() {
            return is_loopback_host(host);
        }
    }
    false
}

fn is_strong_store_secret(secret: &str) -> bool {
    let trimmed = secret.trim();
    if trimmed.len() == 64
        && trimmed.as_bytes().iter().all(|b| b.is_ascii_hexdigit())
        && hex::decode(trimmed)
            .map(|decoded| decoded.len() == 32)
            .unwrap_or(false)
    {
        return true;
    }

    for engine in [STANDARD, STANDARD_NO_PAD, URL_SAFE, URL_SAFE_NO_PAD] {
        if engine
            .decode(trimmed)
            .map(|decoded| decoded.len() >= 32)
            .unwrap_or(false)
        {
            return true;
        }
    }

    false
}

fn is_loopback_host(host: &str) -> bool {
    if host.eq_ignore_ascii_case("localhost") {
        return true;
    }

    host.parse::<IpAddr>()
        .map(|ip| ip.is_loopback())
        .unwrap_or(false)
}

fn read_non_empty_env(name: &str) -> Option<String> {
    std::env::var(name)
        .ok()
        .map(|v| v.trim().to_string())
        .filter(|v| !v.is_empty())
}

fn read_env_bool(name: &'static str, default: bool) -> Result<bool, ConfigError> {
    let Some(raw) = read_non_empty_env(name) else {
        return Ok(default);
    };

    match raw.to_ascii_lowercase().as_str() {
        "1" | "true" | "yes" | "on" => Ok(true),
        "0" | "false" | "no" | "off" => Ok(false),
        _ => Err(ConfigError::InvalidEnv {
            name,
            reason: format!("expected boolean (true/false), got {raw:?}"),
        }),
    }
}

fn read_env_usize(name: &'static str, default: usize, min: usize) -> Result<usize, ConfigError> {
    let Some(raw) = read_non_empty_env(name) else {
        return Ok(default);
    };

    let parsed = raw.parse::<usize>().map_err(|e| ConfigError::InvalidEnv {
        name,
        reason: format!("failed to parse usize from {raw:?}: {e}"),
    })?;
    if parsed < min {
        return Err(ConfigError::InvalidEnv {
            name,
            reason: format!("value {parsed} must be >= {min}"),
        });
    }

    Ok(parsed)
}

fn read_env_u32(name: &'static str, default: u32, min: u32) -> Result<u32, ConfigError> {
    let Some(raw) = read_non_empty_env(name) else {
        return Ok(default);
    };

    let parsed = raw.parse::<u32>().map_err(|e| ConfigError::InvalidEnv {
        name,
        reason: format!("failed to parse u32 from {raw:?}: {e}"),
    })?;
    if parsed < min {
        return Err(ConfigError::InvalidEnv {
            name,
            reason: format!("value {parsed} must be >= {min}"),
        });
    }

    Ok(parsed)
}

fn read_env_u64(name: &'static str, default: u64, min: u64) -> Result<u64, ConfigError> {
    let Some(raw) = read_non_empty_env(name) else {
        return Ok(default);
    };

    let parsed = raw.parse::<u64>().map_err(|e| ConfigError::InvalidEnv {
        name,
        reason: format!("failed to parse u64 from {raw:?}: {e}"),
    })?;
    if parsed < min {
        return Err(ConfigError::InvalidEnv {
            name,
            reason: format!("value {parsed} must be >= {min}"),
        });
    }

    Ok(parsed)
}

fn read_env_f64(name: &'static str, default: f64, min_exclusive: f64) -> Result<f64, ConfigError> {
    let Some(raw) = read_non_empty_env(name) else {
        return Ok(default);
    };

    let parsed = raw.parse::<f64>().map_err(|e| ConfigError::InvalidEnv {
        name,
        reason: format!("failed to parse number from {raw:?}: {e}"),
    })?;
    if !parsed.is_finite() || parsed <= min_exclusive {
        return Err(ConfigError::InvalidEnv {
            name,
            reason: format!("value {parsed} must be finite and > {min_exclusive}"),
        });
    }

    Ok(parsed)
}

#[derive(Debug, thiserror::Error)]
pub enum ConfigError {
    #[error("missing required environment variable: {0}")]
    Missing(&'static str),
    #[error("invalid listen address: {0}")]
    InvalidAddr(String),
    #[error("invalid auth configuration: {0}")]
    InvalidAuthConfig(&'static str),
    #[error("invalid XMR_FROST_CHAIN_NETWORK value: {0} (expected mocknet|public)")]
    InvalidChainNetwork(String),
    #[error(
        "invalid XMR_FROST_STORE_SECRET for public mode: expected 64-char hex (32 bytes) or base64/base64url decoding to at least 32 bytes"
    )]
    InvalidStoreSecretPublic,
    #[error("XMR_FROST_AUTH_BEARER or basic auth credentials are required")]
    MissingAuthConfig,
    #[error("invalid daemon endpoint: {0}")]
    InvalidDaemonEndpoint(String),
    #[error("insecure daemon endpoint: {0}")]
    InsecureDaemonEndpoint(String),
    #[error("invalid environment variable {name}: {reason}")]
    InvalidEnv { name: &'static str, reason: String },
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::{Mutex, MutexGuard};

    // Env vars are process-global; serialize tests that read/write them.
    static ENV_LOCK: Mutex<()> = Mutex::new(());

    fn env_guard() -> MutexGuard<'static, ()> {
        ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner())
    }

    fn reset_env() {
        for name in [
            "XMR_FROST_LISTEN_ADDR",
            "XMR_FROST_DAEMON_URL",
            "XMR_FROST_DAEMON_DISABLE_TLS",
            "XMR_FROST_DAEMON_MAX_RESPONSE_BYTES",
            "XMR_FROST_MAX_DKG_SESSIONS",
            "XMR_FROST_MAX_SIGN_SESSIONS",
            "XMR_FROST_MAX_SIGN_SCAN_LAG_BLOCKS",
            "XMR_FROST_RATE_LIMIT_GLOBAL_RPS",
            "XMR_FROST_RATE_LIMIT_GLOBAL_BURST",
            "XMR_FROST_RATE_LIMIT_PER_IP_RPS",
            "XMR_FROST_RATE_LIMIT_PER_IP_BURST",
            "XMR_FROST_RATE_LIMIT_MAX_TRACKED_IPS",
            "XMR_FROST_DATA_DIR",
            "XMR_FROST_AUTH_USERNAME",
            "XMR_FROST_AUTH_PASSWORD",
            "XMR_FROST_AUTH_BEARER",
            "XMR_FROST_STORE_SECRET",
            "XMR_FROST_CHAIN_NETWORK",
        ] {
            std::env::remove_var(name);
        }
    }

    fn set_minimal_env() {
        std::env::set_var("XMR_FROST_DAEMON_URL", "http://localhost:18081");
        std::env::set_var(
            "XMR_FROST_STORE_SECRET",
            "abababababababababababababababababababababababababababababababab",
        );
        std::env::set_var("XMR_FROST_AUTH_BEARER", "token");
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "mocknet");
    }

    #[test]
    fn test_config_missing_daemon_url() {
        let _guard = env_guard();
        reset_env();
        std::env::set_var("XMR_FROST_STORE_SECRET", "test-secret");
        std::env::set_var("XMR_FROST_AUTH_BEARER", "token");
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "mocknet");

        let err = Config::from_env().unwrap_err();
        assert!(err.to_string().contains("XMR_FROST_DAEMON_URL"));
    }

    #[test]
    fn test_config_missing_store_secret() {
        let _guard = env_guard();
        reset_env();
        std::env::set_var("XMR_FROST_DAEMON_URL", "http://localhost:18081");
        std::env::set_var("XMR_FROST_AUTH_BEARER", "token");
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "mocknet");

        let err = Config::from_env().unwrap_err();
        assert!(err.to_string().contains("XMR_FROST_STORE_SECRET"));
    }

    #[test]
    fn test_config_requires_chain_network() {
        let _guard = env_guard();
        reset_env();
        std::env::set_var("XMR_FROST_DAEMON_URL", "http://localhost:18081");
        std::env::set_var("XMR_FROST_STORE_SECRET", "test-secret");
        std::env::set_var("XMR_FROST_AUTH_BEARER", "token");

        let err = Config::from_env().unwrap_err();
        assert!(err.to_string().contains("XMR_FROST_CHAIN_NETWORK"));
    }

    #[test]
    fn test_config_defaults() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();

        let config = Config::from_env().unwrap();
        assert_eq!(
            config.listen_addr,
            "127.0.0.1:8283".parse::<SocketAddr>().unwrap()
        );
        assert_eq!(config.daemon_url, "http://localhost:18081");
        assert!(!config.daemon_disable_tls);
        assert_eq!(
            config.daemon_max_response_bytes,
            DEFAULT_DAEMON_MAX_RESPONSE_BYTES
        );
        assert_eq!(config.max_dkg_sessions, DEFAULT_MAX_DKG_SESSIONS);
        assert_eq!(config.max_sign_sessions, DEFAULT_MAX_SIGN_SESSIONS);
        assert_eq!(
            config.max_sign_scan_lag_blocks,
            DEFAULT_MAX_SIGN_SCAN_LAG_BLOCKS
        );
        assert_eq!(config.rate_limit_global_rps, DEFAULT_RATE_LIMIT_GLOBAL_RPS);
        assert_eq!(
            config.rate_limit_global_burst,
            DEFAULT_RATE_LIMIT_GLOBAL_BURST
        );
        assert_eq!(config.rate_limit_per_ip_rps, DEFAULT_RATE_LIMIT_PER_IP_RPS);
        assert_eq!(
            config.rate_limit_per_ip_burst,
            DEFAULT_RATE_LIMIT_PER_IP_BURST
        );
        assert_eq!(
            config.rate_limit_max_tracked_ips,
            DEFAULT_RATE_LIMIT_MAX_TRACKED_IPS
        );
        assert_eq!(config.data_dir, PathBuf::from("/var/lib/xmr-frost"));
        assert!(config.auth_username.is_none());
        assert_eq!(config.auth_bearer.as_deref(), Some("token"));
        assert_eq!(config.chain_network, ChainNetworkMode::Mocknet);
    }

    #[test]
    fn test_public_mode_rejects_weak_store_secret() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "public");
        std::env::set_var("XMR_FROST_STORE_SECRET", "weak-secret");

        let result = Config::from_env();
        assert!(matches!(result, Err(ConfigError::InvalidStoreSecretPublic)));
    }

    #[test]
    fn test_public_mode_accepts_hex_store_secret() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "public");
        std::env::set_var(
            "XMR_FROST_STORE_SECRET",
            "abababababababababababababababababababababababababababababababab",
        );

        let result = Config::from_env();
        assert!(result.is_ok());
    }

    #[test]
    fn test_public_mode_accepts_base64_store_secret() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "public");
        let raw = vec![42u8; 32];
        let secret = STANDARD.encode(raw);
        std::env::set_var("XMR_FROST_STORE_SECRET", secret);

        let result = Config::from_env();
        assert!(result.is_ok());
    }

    #[test]
    fn test_mocknet_allows_weak_store_secret() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "mocknet");
        std::env::set_var("XMR_FROST_STORE_SECRET", "weak-secret");

        let result = Config::from_env();
        assert!(result.is_ok());
    }

    #[test]
    fn test_config_requires_auth_even_on_loopback() {
        let _guard = env_guard();
        reset_env();
        std::env::set_var("XMR_FROST_DAEMON_URL", "http://localhost:18081");
        std::env::set_var("XMR_FROST_STORE_SECRET", "test-secret");
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "public");

        let result = Config::from_env();
        assert!(matches!(result, Err(ConfigError::MissingAuthConfig)));
    }

    #[test]
    fn test_config_rejects_partial_basic_auth() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::remove_var("XMR_FROST_AUTH_BEARER");
        std::env::set_var("XMR_FROST_AUTH_USERNAME", "user");

        let result = Config::from_env();
        assert!(matches!(result, Err(ConfigError::InvalidAuthConfig(_))));
    }

    #[test]
    fn test_config_rejects_invalid_chain_network() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "badnet");

        let result = Config::from_env();
        assert!(matches!(result, Err(ConfigError::InvalidChainNetwork(_))));
    }

    #[test]
    fn test_daemon_policy_accepts_loopback_http_in_public_mode() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "public");
        std::env::set_var("XMR_FROST_DAEMON_URL", "http://127.0.0.1:18081");

        let config = Config::from_env().unwrap();
        assert_eq!(config.daemon_url, "http://127.0.0.1:18081");
    }

    #[test]
    fn test_daemon_policy_rejects_non_loopback_http_in_public_mode() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "public");
        std::env::set_var("XMR_FROST_DAEMON_URL", "http://example.com:18081");

        let err = Config::from_env().unwrap_err();
        assert!(matches!(err, ConfigError::InsecureDaemonEndpoint(_)));
    }

    #[test]
    fn test_daemon_policy_error_redacts_userinfo() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "public");
        std::env::set_var(
            "XMR_FROST_DAEMON_URL",
            "http://monero:oxide@example.com:18081/json_rpc",
        );

        let err = Config::from_env().unwrap_err();
        match err {
            ConfigError::InsecureDaemonEndpoint(message) => {
                assert!(message.contains("REDACTED"));
                assert!(!message.contains("monero:oxide"));
            }
            other => panic!("unexpected error: {other}"),
        }
    }

    #[test]
    fn test_daemon_policy_rejects_missing_userinfo_when_tls_required() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "public");
        std::env::set_var("XMR_FROST_DAEMON_URL", "https://example.com:18081");

        let err = Config::from_env().unwrap_err();
        assert!(matches!(err, ConfigError::InsecureDaemonEndpoint(_)));
    }

    #[test]
    fn test_daemon_policy_accepts_non_loopback_with_https_and_userinfo() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "public");
        std::env::set_var(
            "XMR_FROST_DAEMON_URL",
            "https://monero:oxide@example.com:18081/json_rpc",
        );

        let config = Config::from_env().unwrap();
        assert_eq!(config.daemon_url, "https://monero:oxide@example.com:18081");
    }

    #[test]
    fn test_daemon_policy_allows_non_loopback_http_when_tls_disabled() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::set_var("XMR_FROST_CHAIN_NETWORK", "public");
        std::env::set_var("XMR_FROST_DAEMON_DISABLE_TLS", "true");
        std::env::set_var("XMR_FROST_DAEMON_URL", "http://example.com:18081");

        let config = Config::from_env().unwrap();
        assert!(config.daemon_disable_tls);
        assert_eq!(config.daemon_url, "http://example.com:18081");
    }

    #[test]
    fn test_daemon_url_scheme_defaults_by_host() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();

        std::env::set_var("XMR_FROST_DAEMON_URL", "localhost:18081");
        let loopback = Config::from_env().unwrap();
        assert_eq!(loopback.daemon_url, "http://localhost:18081");

        std::env::set_var("XMR_FROST_DAEMON_URL", "monero:oxide@example.com:18081");
        let remote = Config::from_env().unwrap();
        assert_eq!(remote.daemon_url, "https://monero:oxide@example.com:18081");
    }

    #[test]
    fn test_redact_url_userinfo_masks_username_and_password() {
        let redacted = redact_url_userinfo("https://monero:oxide@example.com:18081/json_rpc");
        assert_eq!(
            redacted,
            "https://REDACTED:REDACTED@example.com:18081/json_rpc"
        );
    }

    #[test]
    fn test_redact_url_userinfo_masks_username_without_password() {
        let redacted = redact_url_userinfo("https://monero@example.com:18081");
        assert_eq!(redacted, "https://REDACTED@example.com:18081");
    }

    #[test]
    fn test_redact_url_userinfo_passthrough_invalid_url() {
        let redacted = redact_url_userinfo("not a valid url");
        assert_eq!(redacted, "not a valid url");
    }

    #[test]
    fn test_invalid_limit_env_rejected() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::set_var("XMR_FROST_DAEMON_MAX_RESPONSE_BYTES", "0");

        let err = Config::from_env().unwrap_err();
        assert!(matches!(err, ConfigError::InvalidEnv { .. }));
    }

    #[test]
    fn test_invalid_rate_limit_max_tracked_ips_env_rejected() {
        let _guard = env_guard();
        reset_env();
        set_minimal_env();
        std::env::set_var("XMR_FROST_RATE_LIMIT_MAX_TRACKED_IPS", "0");

        let err = Config::from_env().unwrap_err();
        assert!(matches!(err, ConfigError::InvalidEnv { .. }));
    }
}
