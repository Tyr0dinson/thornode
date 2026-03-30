use std::net::SocketAddr;

use axum::{
    extract::{ConnectInfo, Request},
    http::{header, StatusCode},
    middleware::Next,
    response::Response,
    Extension,
};
use base64::Engine;
use tracing::warn;

/// Authentication configuration.
#[derive(Clone)]
pub struct AuthConfig {
    pub username: Option<String>,
    pub password: Option<String>,
    pub bearer: Option<String>,
}

impl AuthConfig {
    /// Returns true if any authentication is configured.
    pub fn is_enabled(&self) -> bool {
        self.username.is_some() || self.bearer.is_some()
    }

    pub fn disabled() -> Self {
        AuthConfig {
            username: None,
            password: None,
            bearer: None,
        }
    }
}

/// Axum middleware that checks Basic auth or Bearer token.
pub async fn auth_middleware(
    ConnectInfo(addr): ConnectInfo<SocketAddr>,
    auth: Option<Extension<AuthConfig>>,
    request: Request,
    next: Next,
) -> Result<Response, StatusCode> {
    let auth_config = auth
        .map(|Extension(cfg)| cfg)
        .unwrap_or_else(AuthConfig::disabled);

    // All requests on protected routes must authenticate.
    if !auth_config.is_enabled() {
        warn!(remote = %addr, "authentication failed: auth not configured for protected route");
        return Err(StatusCode::UNAUTHORIZED);
    }

    // Try to authenticate
    let auth_header = request
        .headers()
        .get(header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok());

    match auth_header {
        Some(header_value) => {
            if let Some(bearer) = &auth_config.bearer {
                if header_value.starts_with("Bearer ") {
                    let token = &header_value[7..];
                    if constant_time_str_eq(token, bearer) {
                        return Ok(next.run(request).await);
                    }
                }
            }

            if let (Some(username), Some(password)) = (&auth_config.username, &auth_config.password)
            {
                if header_value.starts_with("Basic ") {
                    let encoded = &header_value[6..];
                    if let Ok(decoded) = base64::engine::general_purpose::STANDARD.decode(encoded) {
                        if let Ok(credentials) = String::from_utf8(decoded) {
                            if let Some((user, pass)) = credentials.split_once(':') {
                                if constant_time_str_eq(user, username)
                                    && constant_time_str_eq(pass, password)
                                {
                                    return Ok(next.run(request).await);
                                }
                            }
                        }
                    }
                }
            }

            warn!(remote = %addr, "authentication failed: invalid credentials");
            Err(StatusCode::UNAUTHORIZED)
        }
        None => {
            warn!(remote = %addr, "authentication failed: no authorization header");
            Err(StatusCode::UNAUTHORIZED)
        }
    }
}

/// Constant-time string comparison to prevent timing attacks.
fn constant_time_str_eq(a: &str, b: &str) -> bool {
    let a_bytes = a.as_bytes();
    let b_bytes = b.as_bytes();
    let max_len = a_bytes.len().max(b_bytes.len());
    let mut diff = (a_bytes.len() as u64) ^ (b_bytes.len() as u64);

    for i in 0..max_len {
        let lhs = a_bytes.get(i).copied().unwrap_or_default();
        let rhs = b_bytes.get(i).copied().unwrap_or_default();
        diff |= u64::from(lhs ^ rhs);
    }

    diff == 0
}

#[cfg(test)]
mod tests {
    use super::*;

    use axum::{
        body::Body, extract::connect_info::MockConnectInfo, http::Request, middleware::from_fn,
        routing::get, Router,
    };
    use tower::ServiceExt;

    #[tokio::test]
    async fn non_loopback_requires_auth_header() {
        let app = Router::new()
            .route("/secure", get(|| async { StatusCode::OK }))
            .layer(from_fn(auth_middleware))
            .layer(Extension(AuthConfig {
                username: None,
                password: None,
                bearer: Some("secret-token".to_string()),
            }))
            .layer(MockConnectInfo(SocketAddr::from(([10, 0, 0, 2], 12345))));

        let response = app
            .oneshot(
                Request::builder()
                    .uri("/secure")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
    }

    #[tokio::test]
    async fn bearer_auth_allows_request() {
        let app = Router::new()
            .route("/secure", get(|| async { StatusCode::OK }))
            .layer(from_fn(auth_middleware))
            .layer(Extension(AuthConfig {
                username: None,
                password: None,
                bearer: Some("secret-token".to_string()),
            }))
            .layer(MockConnectInfo(SocketAddr::from(([10, 0, 0, 2], 12345))));

        let response = app
            .oneshot(
                Request::builder()
                    .uri("/secure")
                    .header("Authorization", "Bearer secret-token")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::OK);
    }

    #[test]
    fn constant_time_str_eq_rejects_mismatched_lengths() {
        assert!(!constant_time_str_eq("short", "much-longer"));
        assert!(!constant_time_str_eq("much-longer", "short"));
        assert!(constant_time_str_eq("same-length", "same-length"));
    }

    #[test]
    fn constant_time_str_eq_rejects_large_length_differences() {
        let short = "a".repeat(44);
        let long = "b".repeat(300);
        let much_longer = "c".repeat(512);

        assert!(!constant_time_str_eq(&short, &long));
        assert!(!constant_time_str_eq(&long, &short));
        assert!(!constant_time_str_eq("", &much_longer));
    }

    #[tokio::test]
    async fn loopback_requires_auth() {
        let app = Router::new()
            .route("/secure", get(|| async { StatusCode::OK }))
            .layer(from_fn(auth_middleware))
            .layer(Extension(AuthConfig::disabled()))
            .layer(MockConnectInfo(SocketAddr::from(([127, 0, 0, 1], 12345))));

        let response = app
            .oneshot(
                Request::builder()
                    .uri("/secure")
                    .body(Body::empty())
                    .unwrap(),
            )
            .await
            .unwrap();
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED);
    }
}
