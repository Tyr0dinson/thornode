use anyhow::{Context, Result};
use monero_simple_request_rpc::SimpleRequestRpc;
use serde::{Deserialize, Serialize};
use tracing::debug;

/// Client for Monero daemon JSON-RPC and binary endpoints.
#[derive(Clone)]
pub struct DaemonClient {
    url: String,
    client: reqwest::Client,
    max_response_bytes: usize,
}

pub type OxideRpc = SimpleRequestRpc;

// ---- JSON-RPC types ----

#[derive(Serialize)]
struct JsonRpcRequest<P: Serialize> {
    jsonrpc: &'static str,
    id: &'static str,
    method: &'static str,
    params: P,
}

#[derive(Deserialize)]
struct JsonRpcResponse<R> {
    result: Option<R>,
    error: Option<JsonRpcError>,
}

#[derive(Deserialize, Debug)]
struct JsonRpcError {
    code: i64,
    message: String,
}

// ---- Response types ----

#[derive(Deserialize, Debug)]
pub struct BlockHeaderResponse {
    pub block_header: BlockHeader,
}

#[derive(Deserialize, Debug, Clone)]
pub struct BlockHeader {
    pub height: u64,
    pub hash: String,
    #[serde(default)]
    pub prev_hash: String,
    pub timestamp: u64,
    pub major_version: u8,
    pub minor_version: u8,
    pub nonce: u64,
    pub reward: u64,
    pub num_txes: u64,
}

#[derive(Deserialize, Debug)]
pub struct BlockResponse {
    pub block_header: BlockHeader,
    pub tx_hashes: Option<Vec<String>>,
    pub blob: Option<String>,
}

#[derive(Serialize)]
struct GetBlockParams {
    height: u64,
}

#[derive(Serialize)]
struct GetBlockHeaderByHeightParams {
    height: u64,
}

#[derive(Serialize)]
struct EmptyParams {}

#[derive(Deserialize, Debug)]
pub struct GetInfoResponse {
    pub height: u64,
    pub top_block_hash: String,
    pub target_height: u64,
    pub difficulty: u64,
    pub status: String,
}

#[derive(Deserialize, Debug)]
pub struct FeeEstimateResponse {
    pub fee: u64,
    pub quantization_mask: u64,
    pub status: String,
}

#[derive(Serialize)]
struct GetTransactionsRequest {
    txs_hashes: Vec<String>,
    decode_as_json: bool,
}

#[derive(Deserialize, Debug)]
pub struct GetTransactionsResponse {
    pub txs: Option<Vec<TxEntry>>,
    #[serde(default)]
    pub missed_tx: Option<Vec<String>>,
    pub status: String,
}

#[derive(Deserialize, Debug, Clone)]
pub struct TxEntry {
    pub tx_hash: String,
    pub as_hex: String,
    pub as_json: Option<String>,
    pub block_height: Option<u64>,
    pub in_pool: Option<bool>,
}

#[derive(Serialize)]
struct SendRawTxRequest {
    tx_as_hex: String,
    do_not_relay: bool,
}

#[derive(Deserialize, Debug)]
pub struct SendRawTxResponse {
    pub status: String,
    pub tx_hash: Option<String>,
    pub tx_hash_list: Option<Vec<String>>,
    pub double_spend: Option<bool>,
    pub fee_too_low: Option<bool>,
    pub reason: Option<String>,
}

#[derive(Deserialize, Debug)]
pub struct OutputDistribution {
    pub distribution: Vec<u64>,
    pub start_height: u64,
    pub base: u64,
}

#[derive(Deserialize, Debug)]
pub struct GetOutputDistributionResponse {
    pub distributions: Vec<OutputDistribution>,
    pub status: String,
}

#[derive(Serialize)]
struct GetOutputDistributionParams {
    amounts: Vec<u64>,
    from_height: u64,
    to_height: u64,
    cumulative: bool,
}

#[derive(Serialize)]
struct GetOutputsRequest {
    outputs: Vec<OutputIndex>,
}

#[derive(Serialize)]
pub struct OutputIndex {
    pub amount: u64,
    pub index: u64,
}

#[derive(Deserialize, Debug)]
pub struct GetOutputsResponse {
    pub outs: Vec<OutputEntry>,
    pub status: String,
}

#[derive(Deserialize, Debug, Clone)]
pub struct OutputEntry {
    pub height: u64,
    pub key: String,
    pub mask: String,
    pub txid: String,
    pub unlocked: bool,
}

impl DaemonClient {
    pub fn new(url: &str, max_response_bytes: usize) -> Self {
        let client = reqwest::Client::builder()
            .timeout(std::time::Duration::from_secs(30))
            .build()
            .expect("failed to build HTTP client");

        DaemonClient {
            url: url.trim_end_matches('/').to_string(),
            client,
            max_response_bytes,
        }
    }

    pub fn url(&self) -> &str {
        &self.url
    }

    pub async fn oxide_rpc(&self) -> Result<OxideRpc> {
        SimpleRequestRpc::new(self.url.clone())
            .await
            .map_err(|e| anyhow::anyhow!("failed to initialize monero-oxide RPC client: {}", e))
    }

    async fn read_limited_body(
        &self,
        mut resp: reqwest::Response,
        context: &str,
    ) -> Result<Vec<u8>> {
        let mut body = Vec::new();
        while let Some(chunk) = resp
            .chunk()
            .await
            .with_context(|| format!("failed to read {context} response chunk"))?
        {
            if body.len().saturating_add(chunk.len()) > self.max_response_bytes {
                anyhow::bail!(
                    "{} response exceeded configured limit ({} bytes)",
                    context,
                    self.max_response_bytes
                );
            }
            body.extend_from_slice(&chunk);
        }
        Ok(body)
    }

    /// Call a JSON-RPC method on /json_rpc.
    async fn json_rpc<P: Serialize, R: for<'de> Deserialize<'de>>(
        &self,
        method: &'static str,
        params: P,
    ) -> Result<R> {
        let url = format!("{}/json_rpc", self.url);
        let body = JsonRpcRequest {
            jsonrpc: "2.0",
            id: "0",
            method,
            params,
        };

        debug!(method, "daemon JSON-RPC request");

        let resp = self
            .client
            .post(&url)
            .json(&body)
            .send()
            .await
            .with_context(|| format!("daemon RPC {} failed", method))?;

        let status = resp.status();
        if !status.is_success() {
            let body = self
                .read_limited_body(resp, &format!("daemon RPC {method}"))
                .await?;
            let text = String::from_utf8_lossy(&body);
            anyhow::bail!("daemon RPC {} returned {}: {}", method, status, text);
        }

        let body = self
            .read_limited_body(resp, &format!("daemon RPC {method}"))
            .await?;
        let rpc_resp: JsonRpcResponse<R> = serde_json::from_slice(&body)
            .with_context(|| format!("failed to decode daemon RPC {method} JSON response"))?;
        if let Some(err) = rpc_resp.error {
            anyhow::bail!("daemon RPC {} error {}: {}", method, err.code, err.message);
        }

        rpc_resp
            .result
            .ok_or_else(|| anyhow::anyhow!("daemon RPC {} returned no result", method))
    }

    /// Call a binary/other endpoint on the daemon.
    async fn other_rpc<P: Serialize, R: for<'de> Deserialize<'de>>(
        &self,
        path: &str,
        params: P,
    ) -> Result<R> {
        let url = format!("{}/{}", self.url, path.trim_start_matches('/'));

        let resp = self
            .client
            .post(&url)
            .json(&params)
            .send()
            .await
            .with_context(|| format!("daemon {} request failed", path))?;

        let status = resp.status();
        if !status.is_success() {
            let body = self
                .read_limited_body(resp, &format!("daemon {path}"))
                .await?;
            let text = String::from_utf8_lossy(&body);
            anyhow::bail!("daemon {} returned {}: {}", path, status, text);
        }

        let body = self
            .read_limited_body(resp, &format!("daemon {path}"))
            .await?;
        let result: R = serde_json::from_slice(&body)
            .with_context(|| format!("failed to decode daemon {path} JSON response"))?;
        Ok(result)
    }

    // ---- Public API ----

    /// Get current blockchain info.
    pub async fn get_info(&self) -> Result<GetInfoResponse> {
        self.json_rpc("get_info", EmptyParams {}).await
    }

    /// Get the current blockchain height.
    pub async fn get_height(&self) -> Result<u64> {
        let info = self.get_info().await?;
        Ok(info.height)
    }

    /// Get block header by height.
    pub async fn get_block_header_by_height(&self, height: u64) -> Result<BlockHeaderResponse> {
        self.json_rpc(
            "get_block_header_by_height",
            GetBlockHeaderByHeightParams { height },
        )
        .await
    }

    /// Get block (header + tx hashes) by height.
    pub async fn get_block_by_height(&self, height: u64) -> Result<BlockResponse> {
        self.json_rpc("get_block", GetBlockParams { height }).await
    }

    /// Get the latest block header.
    pub async fn get_last_block_header(&self) -> Result<BlockHeaderResponse> {
        self.json_rpc("get_last_block_header", EmptyParams {}).await
    }

    /// Get transactions by hash.
    pub async fn get_transactions(&self, tx_hashes: &[String]) -> Result<GetTransactionsResponse> {
        self.other_rpc(
            "/get_transactions",
            GetTransactionsRequest {
                txs_hashes: tx_hashes.to_vec(),
                decode_as_json: true,
            },
        )
        .await
    }

    /// Get fee estimate from the daemon.
    pub async fn get_fee_estimate(&self) -> Result<FeeEstimateResponse> {
        self.json_rpc("get_fee_estimate", EmptyParams {}).await
    }

    /// Submit a raw transaction to the network.
    pub async fn send_raw_transaction(&self, tx_hex: &str) -> Result<SendRawTxResponse> {
        self.other_rpc(
            "/send_raw_transaction",
            SendRawTxRequest {
                tx_as_hex: tx_hex.to_string(),
                do_not_relay: false,
            },
        )
        .await
    }

    /// Get output distribution for RCT outputs (amount=0).
    pub async fn get_output_distribution(
        &self,
        from_height: u64,
        to_height: u64,
    ) -> Result<GetOutputDistributionResponse> {
        self.json_rpc(
            "get_output_distribution",
            GetOutputDistributionParams {
                amounts: vec![0],
                from_height,
                to_height,
                cumulative: true,
            },
        )
        .await
    }

    /// Get specific outputs by global index.
    pub async fn get_outs(&self, outputs: Vec<OutputIndex>) -> Result<GetOutputsResponse> {
        self.other_rpc("/get_outs", GetOutputsRequest { outputs })
            .await
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    use axum::{
        body::Body,
        http::StatusCode,
        response::{IntoResponse, Response},
        routing::post,
        Router,
    };

    const TEST_TIMEOUT: std::time::Duration = std::time::Duration::from_secs(3);

    async fn spawn_test_server(app: Router) -> String {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move {
            axum::serve(listener, app).await.unwrap();
        });
        format!("http://{}", addr)
    }

    fn info_result() -> serde_json::Value {
        serde_json::json!({
            "result": {
                "height": 1,
                "top_block_hash": "abc123",
                "target_height": 1,
                "difficulty": 1,
                "status": "OK"
            }
        })
    }

    #[tokio::test]
    async fn json_rpc_succeeds_within_response_limit() {
        let app = Router::new().route(
            "/json_rpc",
            post(|| async move { (StatusCode::OK, axum::Json(info_result())) }),
        );
        let base_url = spawn_test_server(app).await;
        let client = DaemonClient::new(&base_url, 1024);

        let info = tokio::time::timeout(TEST_TIMEOUT, client.get_info())
            .await
            .unwrap()
            .unwrap();
        assert_eq!(info.height, 1);
        assert_eq!(info.status, "OK");
    }

    #[tokio::test]
    async fn json_rpc_rejects_oversized_success_response() {
        let app = Router::new().route(
            "/json_rpc",
            post(|| async move {
                let large = "a".repeat(512);
                let body = serde_json::json!({
                    "result": {
                        "height": 1,
                        "top_block_hash": large,
                        "target_height": 1,
                        "difficulty": 1,
                        "status": "OK"
                    }
                });
                (StatusCode::OK, axum::Json(body)).into_response()
            }),
        );
        let base_url = spawn_test_server(app).await;
        let client = DaemonClient::new(&base_url, 128);

        let err = tokio::time::timeout(TEST_TIMEOUT, client.get_info())
            .await
            .unwrap()
            .unwrap_err();
        assert!(err.to_string().contains("exceeded configured limit"));
    }

    #[tokio::test]
    async fn json_rpc_rejects_oversized_error_response() {
        async fn oversized_error(_: axum::http::Request<Body>) -> Response {
            let body = "x".repeat(512);
            (StatusCode::INTERNAL_SERVER_ERROR, body).into_response()
        }

        let app = Router::new().route("/json_rpc", post(oversized_error));
        let base_url = spawn_test_server(app).await;
        let client = DaemonClient::new(&base_url, 128);

        let err = tokio::time::timeout(TEST_TIMEOUT, client.get_info())
            .await
            .unwrap()
            .unwrap_err();
        assert!(err.to_string().contains("exceeded configured limit"));
    }

    #[tokio::test]
    async fn get_transactions_decodes_optional_missed_tx() {
        let app = Router::new().route(
            "/get_transactions",
            post(|| async move {
                (
                    StatusCode::OK,
                    axum::Json(serde_json::json!({
                        "txs": [],
                        "missed_tx": ["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],
                        "status": "OK"
                    })),
                )
            }),
        );
        let base_url = spawn_test_server(app).await;
        let client = DaemonClient::new(&base_url, 1024);

        let hashes =
            vec!["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa".to_string()];
        let resp = tokio::time::timeout(TEST_TIMEOUT, client.get_transactions(&hashes))
            .await
            .unwrap()
            .unwrap();
        let missed = resp.missed_tx.unwrap_or_default();
        assert_eq!(missed.len(), 1);
    }
}
