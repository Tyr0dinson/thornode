use std::collections::HashMap;
use std::net::{IpAddr, SocketAddr};
use std::sync::Arc;
use std::time::{Duration, Instant};

use axum::{
    extract::{ConnectInfo, Request},
    http::StatusCode,
    middleware::Next,
    response::{IntoResponse, Response},
    Extension, Json,
};
use parking_lot::Mutex;

use crate::api::types::ErrorResponse;

const PER_IP_IDLE_TTL: Duration = Duration::from_secs(600);
const PRUNE_INTERVAL: Duration = Duration::from_secs(60);

#[derive(Debug, Clone, Copy)]
pub struct RateLimitConfig {
    pub global_rps: f64,
    pub global_burst: u32,
    pub per_ip_rps: f64,
    pub per_ip_burst: u32,
    pub max_tracked_ips: usize,
}

#[derive(Clone)]
pub struct RateLimiter {
    inner: Arc<Mutex<LimiterState>>,
}

struct LimiterState {
    global: TokenBucket,
    per_ip: HashMap<IpAddr, PerIpBucket>,
    last_prune_at: Instant,
    config: RateLimitConfig,
}

#[derive(Clone)]
struct TokenBucket {
    tokens: f64,
    capacity: f64,
    refill_per_sec: f64,
    last_refill: Instant,
}

struct PerIpBucket {
    bucket: TokenBucket,
    last_seen: Instant,
}

impl RateLimiter {
    pub fn new(config: RateLimitConfig) -> Self {
        let now = Instant::now();
        RateLimiter {
            inner: Arc::new(Mutex::new(LimiterState {
                global: TokenBucket::new(config.global_burst as f64, config.global_rps, now),
                per_ip: HashMap::new(),
                last_prune_at: now,
                config,
            })),
        }
    }

    pub fn allow(&self, ip: IpAddr) -> bool {
        let now = Instant::now();
        let mut state = self.inner.lock();
        if now.duration_since(state.last_prune_at) >= PRUNE_INTERVAL {
            state
                .per_ip
                .retain(|_, peer| now.duration_since(peer.last_seen) < PER_IP_IDLE_TTL);
            state.last_prune_at = now;
        }

        if !state.global.try_consume(now, 1.0) {
            return false;
        }

        let config = state.config;
        if !state.per_ip.contains_key(&ip) && state.per_ip.len() >= config.max_tracked_ips {
            state.global.refund(1.0);
            return false;
        }
        let peer = state.per_ip.entry(ip).or_insert_with(|| PerIpBucket {
            bucket: TokenBucket::new(config.per_ip_burst as f64, config.per_ip_rps, now),
            last_seen: now,
        });
        peer.last_seen = now;

        if !peer.bucket.try_consume(now, 1.0) {
            state.global.refund(1.0);
            return false;
        }

        true
    }
}

impl TokenBucket {
    fn new(capacity: f64, refill_per_sec: f64, now: Instant) -> Self {
        TokenBucket {
            tokens: capacity,
            capacity,
            refill_per_sec,
            last_refill: now,
        }
    }

    fn try_consume(&mut self, now: Instant, amount: f64) -> bool {
        self.refill(now);
        if self.tokens >= amount {
            self.tokens -= amount;
            true
        } else {
            false
        }
    }

    fn refund(&mut self, amount: f64) {
        self.tokens = (self.tokens + amount).min(self.capacity);
    }

    fn refill(&mut self, now: Instant) {
        let elapsed = now.duration_since(self.last_refill).as_secs_f64();
        if elapsed > 0.0 && self.refill_per_sec > 0.0 {
            let refilled = self.tokens + (elapsed * self.refill_per_sec);
            self.tokens = refilled.min(self.capacity);
        }
        self.last_refill = now;
    }
}

pub async fn rate_limit_middleware(
    ConnectInfo(addr): ConnectInfo<SocketAddr>,
    limiter: Option<Extension<RateLimiter>>,
    request: Request,
    next: Next,
) -> Response {
    let Some(Extension(limiter)) = limiter else {
        return (
            StatusCode::INTERNAL_SERVER_ERROR,
            Json(ErrorResponse::new("rate limiter not configured")),
        )
            .into_response();
    };

    if !limiter.allow(addr.ip()) {
        return (
            StatusCode::TOO_MANY_REQUESTS,
            Json(ErrorResponse::new("rate limit exceeded")),
        )
            .into_response();
    }

    next.run(request).await
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::api::auth::{auth_middleware, AuthConfig};

    use axum::{
        body::Body, extract::connect_info::MockConnectInfo, http::Request, middleware::from_fn,
        routing::get, Router,
    };
    use tower::ServiceExt;

    fn app_for_ip(limiter: RateLimiter, ip: [u8; 4]) -> Router {
        Router::new()
            .route("/secure", get(|| async { StatusCode::OK }))
            .layer(from_fn(rate_limit_middleware))
            .layer(Extension(limiter))
            .layer(MockConnectInfo(SocketAddr::from((ip, 12345))))
    }

    fn protected_app_for_ip(limiter: RateLimiter, ip: [u8; 4]) -> Router {
        Router::new()
            .route("/secure", get(|| async { StatusCode::OK }))
            // Production order: auth executes before rate limiting.
            .layer(from_fn(rate_limit_middleware))
            .layer(Extension(limiter))
            .layer(from_fn(auth_middleware))
            .layer(Extension(AuthConfig {
                username: None,
                password: None,
                bearer: Some("secret-token".to_string()),
            }))
            .layer(MockConnectInfo(SocketAddr::from((ip, 12345))))
    }

    async fn do_request(app: Router, auth_header: Option<&str>) -> StatusCode {
        let mut builder = Request::builder().uri("/secure");
        if let Some(value) = auth_header {
            builder = builder.header("Authorization", value);
        }
        app.oneshot(builder.body(Body::empty()).unwrap())
            .await
            .unwrap()
            .status()
    }

    #[tokio::test]
    async fn burst_exhaustion_returns_429() {
        let limiter = RateLimiter::new(RateLimitConfig {
            global_rps: 100.0,
            global_burst: 100,
            per_ip_rps: 0.1,
            per_ip_burst: 1,
            max_tracked_ips: 1024,
        });
        let app = app_for_ip(limiter, [10, 0, 0, 1]);

        assert_eq!(do_request(app.clone(), None).await, StatusCode::OK);
        assert_eq!(do_request(app, None).await, StatusCode::TOO_MANY_REQUESTS);
    }

    #[tokio::test]
    async fn per_ip_buckets_are_independent() {
        let limiter = RateLimiter::new(RateLimitConfig {
            global_rps: 100.0,
            global_burst: 100,
            per_ip_rps: 0.1,
            per_ip_burst: 1,
            max_tracked_ips: 1024,
        });
        let app_1 = app_for_ip(limiter.clone(), [10, 0, 0, 1]);
        let app_2 = app_for_ip(limiter, [10, 0, 0, 2]);

        assert_eq!(do_request(app_1, None).await, StatusCode::OK);
        assert_eq!(do_request(app_2, None).await, StatusCode::OK);
    }

    #[tokio::test]
    async fn global_bucket_exhaustion_affects_all_clients() {
        let limiter = RateLimiter::new(RateLimitConfig {
            global_rps: 0.1,
            global_burst: 1,
            per_ip_rps: 100.0,
            per_ip_burst: 100,
            max_tracked_ips: 1024,
        });
        let app_1 = app_for_ip(limiter.clone(), [10, 0, 0, 1]);
        let app_2 = app_for_ip(limiter, [10, 0, 0, 2]);

        assert_eq!(do_request(app_1, None).await, StatusCode::OK);
        assert_eq!(do_request(app_2, None).await, StatusCode::TOO_MANY_REQUESTS);
    }

    #[tokio::test]
    async fn tokens_refill_after_interval() {
        let limiter = RateLimiter::new(RateLimitConfig {
            global_rps: 100.0,
            global_burst: 100,
            per_ip_rps: 20.0,
            per_ip_burst: 1,
            max_tracked_ips: 1024,
        });
        let app = app_for_ip(limiter, [10, 0, 0, 1]);

        assert_eq!(do_request(app.clone(), None).await, StatusCode::OK);
        assert_eq!(
            do_request(app.clone(), None).await,
            StatusCode::TOO_MANY_REQUESTS
        );
        tokio::time::sleep(Duration::from_millis(80)).await;
        assert_eq!(do_request(app, None).await, StatusCode::OK);
    }

    #[tokio::test]
    async fn unauthenticated_requests_do_not_consume_rate_limit_budget_when_auth_runs_first() {
        let limiter = RateLimiter::new(RateLimitConfig {
            global_rps: 100.0,
            global_burst: 100,
            per_ip_rps: 0.1,
            per_ip_burst: 1,
            max_tracked_ips: 1024,
        });
        let app = protected_app_for_ip(limiter, [10, 0, 0, 9]);

        assert_eq!(
            do_request(app.clone(), None).await,
            StatusCode::UNAUTHORIZED
        );
        assert_eq!(
            do_request(app, Some("Bearer secret-token")).await,
            StatusCode::OK
        );
    }

    #[test]
    fn max_tracked_ips_blocks_new_ips_when_capacity_reached() {
        let limiter = RateLimiter::new(RateLimitConfig {
            global_rps: 100.0,
            global_burst: 100,
            per_ip_rps: 100.0,
            per_ip_burst: 100,
            max_tracked_ips: 1,
        });

        let ip_a: IpAddr = [10, 0, 0, 1].into();
        let ip_b: IpAddr = [10, 0, 0, 2].into();

        assert!(limiter.allow(ip_a));
        assert!(!limiter.allow(ip_b));
    }

    #[test]
    fn max_tracked_ips_preserves_existing_ip_behavior() {
        let limiter = RateLimiter::new(RateLimitConfig {
            global_rps: 100.0,
            global_burst: 100,
            per_ip_rps: 100.0,
            per_ip_burst: 2,
            max_tracked_ips: 1,
        });
        let ip_a: IpAddr = [10, 0, 0, 1].into();
        let ip_b: IpAddr = [10, 0, 0, 2].into();

        assert!(limiter.allow(ip_a));
        assert!(limiter.allow(ip_a));
        assert!(!limiter.allow(ip_a));
        assert!(!limiter.allow(ip_b));
    }

    #[test]
    fn max_tracked_ips_capacity_is_recovered_after_prune() {
        let limiter = RateLimiter::new(RateLimitConfig {
            global_rps: 100.0,
            global_burst: 100,
            per_ip_rps: 100.0,
            per_ip_burst: 100,
            max_tracked_ips: 1,
        });
        let ip_a: IpAddr = [10, 0, 0, 1].into();
        let ip_b: IpAddr = [10, 0, 0, 2].into();

        assert!(limiter.allow(ip_a));
        assert!(!limiter.allow(ip_b));

        {
            let mut state = limiter.inner.lock();
            let stale_age = PRUNE_INTERVAL + PER_IP_IDLE_TTL + Duration::from_millis(10);
            if let Some(peer) = state.per_ip.get_mut(&ip_a) {
                peer.last_seen = Instant::now() - stale_age;
            }
            state.last_prune_at = Instant::now() - PRUNE_INTERVAL - Duration::from_millis(1);
        }

        assert!(limiter.allow(ip_b));
    }
}
