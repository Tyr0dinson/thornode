use std::collections::HashMap;

use serde::{Deserialize, Serialize};

// ---- DKG Request/Response types ----

#[derive(Debug, Deserialize)]
pub struct DkgRound1Request {
    pub vault_pub_key: String,
    pub threshold: u16,
    pub n: u16,
    pub participant_index: u16,
    pub context: String,
    pub session_id: String,
}

#[derive(Debug, Serialize)]
pub struct DkgRound1Response {
    pub commitments: String, // hex
}

#[derive(Debug, Deserialize)]
pub struct DkgRound2Request {
    pub session_id: String,
    pub all_commitments: HashMap<String, String>, // participant_index -> hex
}

#[derive(Debug, Serialize)]
pub struct DkgRound2Response {
    pub encrypted_shares: HashMap<String, String>, // participant_index -> hex
}

#[derive(Debug, Deserialize)]
pub struct DkgRound3Request {
    pub session_id: String,
    pub all_shares: HashMap<String, String>, // participant_index -> hex
}

#[derive(Debug, Serialize)]
pub struct DkgRound3Response {
    pub address: String,
    pub group_key: String,
}

#[derive(Debug, Deserialize)]
pub struct DkgFinalizeRequest {
    pub session_id: String,
    pub group_key: String,
    pub address: String,
}

#[derive(Debug, Serialize)]
pub struct DkgFinalizeResponse {
    pub success: bool,
}

// ---- Signing Request/Response types ----

#[derive(Debug, Deserialize, Clone, Serialize)]
pub struct SignPrepareRequest {
    #[serde(default)]
    pub msg_id: String,
    pub vault_pub_key: String,
    pub to_address: String,
    pub amount_atomic: u64,
    pub memo: Option<String>,
    pub fee_rate: Option<u64>,
    pub decoy_context: String,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct SignPrepareResponse {
    pub session_id: String,
    pub preprocess: String, // hex
}

#[derive(Debug, Deserialize, Clone, Serialize)]
pub struct SignPartialRequest {
    #[serde(default)]
    pub msg_id: String,
    pub session_id: String,
    pub all_preprocesses: HashMap<String, String>, // participant_index -> hex
}

#[derive(Debug, Serialize, Deserialize)]
pub struct SignPartialResponse {
    pub signature_share: String, // hex
}

#[derive(Debug, Deserialize, Clone, Serialize)]
pub struct SignCombineRequest {
    #[serde(default)]
    pub msg_id: String,
    pub session_id: String,
    pub all_shares: HashMap<String, String>, // participant_index -> hex
}

#[derive(Debug, Serialize)]
pub struct SignCombineResponse {
    pub tx_hash: String,
    pub tx_hex: String,
    pub fee_atomic: u64,
}

#[derive(Debug, Deserialize, Clone, Serialize)]
pub struct BroadcastRequest {
    pub tx_hex: String,
    #[serde(default)]
    pub tx_hash: String,
    #[serde(default)]
    pub vault_pub_key: String,
    #[serde(default)]
    pub msg_id: String,
}

#[derive(Debug, Serialize)]
pub struct BroadcastResponse {
    pub tx_hash: String,
}

// ---- Scanner/Balance types ----

#[derive(Debug, Serialize)]
pub struct BalanceResponse {
    pub balance: u64,
    pub unlocked_balance: u64,
}

#[derive(Debug, Serialize)]
pub struct TransferEntry {
    pub txid: String,
    pub address: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub from_address: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub vault_pub_key: Option<String>,
    pub amount: u64,
    pub fee: u64,
    pub height: u64,
    pub confirmations: u64,
    pub note: String,
    pub locked: bool,
}

#[derive(Debug, Serialize)]
pub struct TransfersResponse {
    pub incoming: Vec<TransferEntry>,
    pub outgoing: Vec<TransferEntry>,
}

#[derive(Debug, Serialize)]
pub struct AddressesResponse {
    pub addresses: Vec<String>,
}

#[derive(Debug, Serialize)]
pub struct OutputEntry {
    pub tx_hash: String,
    pub output_index: u32,
    pub amount: u64,
    pub height: u64,
    pub key_image: Option<String>,
    pub spent: bool,
    #[serde(default)]
    pub state: String,
    pub subaddress_major: u32,
    pub subaddress_minor: u32,
}

#[derive(Debug, Serialize)]
pub struct OutputsResponse {
    pub outputs: Vec<OutputEntry>,
}

#[derive(Debug, Serialize)]
pub struct FeeRateResponse {
    pub fee_rate_per_weight: u64,
}

#[derive(Debug, Serialize)]
pub struct ChainHeightResponse {
    pub height: u64,
}

#[derive(Debug, Serialize)]
pub struct ChainBlockResponse {
    pub height: u64,
    pub reward: u64,
    pub hash: String,
    pub previous_hash: String,
    pub tx_hashes: Vec<String>,
}

#[derive(Debug, Serialize)]
pub struct ChainTxExistsResponse {
    pub exists: bool,
}

#[derive(Debug, Deserialize)]
pub struct ScannerSyncRequest {
    pub height: u64,
    #[allow(dead_code)]
    // Accepted for wire compatibility; scanner sync logic derives confirmations from validated height.
    pub tip_height: u64,
    pub block_hash: String,
    pub parent_hash: String,
    pub reorg_window: u64,
}

// ---- Simulation helper types ----

#[derive(Debug, Deserialize, Serialize)]
pub struct SimBalanceRequest {
    pub spend_key_hex: String,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct SimBalanceResponse {
    pub address: String,
    pub balance_atomic: u64,
    pub unlocked_balance_atomic: u64,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct SimTransferRequest {
    pub spend_key_hex: String,
    pub to_address: String,
    pub amount_atomic: u64,
    pub memo: Option<String>,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct SimFaucetRequest {
    pub to_address: String,
    pub amount_atomic: u64,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct SimTransferResponse {
    pub tx_hash: String,
}

// ---- Error response ----

#[derive(Debug, Serialize, Deserialize)]
pub struct ErrorResponse {
    pub error: String,
}

impl ErrorResponse {
    pub fn new(msg: impl Into<String>) -> Self {
        ErrorResponse { error: msg.into() }
    }
}
