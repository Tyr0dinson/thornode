use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::Arc;

use axum::{
    body::to_bytes, extract::State, http::StatusCode, response::IntoResponse, routing::post, Json,
    Router,
};
use bech32::{decode as bech32_decode, FromBase32};
use serde::Deserialize;
use tempfile::TempDir;

use super::routes::{list_vault_states, register_vault, AppState};
use super::types::{KeyImageEntry, RegisterTxKeyEntry, RegisterVaultRequest};
use crate::config::ChainNetworkMode;
use crate::frost::dkg::DkgCoordinator;
use crate::frost::key_image::KeyImageCoordinator;
use crate::frost::sign::SignCoordinator;
use crate::state::store::OutgoingTransferRecord;
use crate::state::store::OutgoingTransferStatus;
use crate::state::{Store, VaultLifecycle};
use crate::tx::daemon::DaemonClient;
use crate::tx::scanner::Scanner;

const REPLAY_BUNDLE_DIR_ENV: &str = "XMR_REPLAY_BUNDLE_DIR";
const ED25519_PUBKEY_AMINO_PREFIX: &[u8] = &[0x16, 0x24, 0xde, 0x64, 0x20];

#[derive(Debug, Deserialize)]
struct ReplayBundle {
    asgards: Vec<ReplayAsgard>,
    daemon_txs: HashMap<String, serde_json::Value>,
    node_states: HashMap<String, ReplayNodeState>,
}

#[derive(Debug, Deserialize)]
struct ReplayAsgard {
    addresses: Vec<ReplayAddress>,
    #[serde(default)]
    key_images_xmr: Vec<ReplayKeyImageRef>,
    #[serde(default)]
    tx_keys_xmr: Vec<ReplayBundleTxKeyRef>,
    pub_key_frost: String,
    vault_creation_height_xmr: u64,
}

#[derive(Debug, Deserialize)]
struct ReplayAddress {
    address: String,
    chain: String,
}

#[derive(Debug, Deserialize, Clone)]
struct ReplayKeyImageRef {
    tx_hash: String,
    #[serde(default)]
    output_index: u32,
    key_image: String,
}

#[derive(Debug, Deserialize, Clone)]
struct ReplayBundleTxKeyRef {
    tx_hash: String,
    tx_keys: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct ReplayNodeState {
    #[serde(default)]
    outbounds: HashMap<String, Vec<ReplayOutbound>>,
}

#[derive(Debug, Deserialize, Clone)]
struct ReplayOutbound {
    tx_hash: String,
    tx_keys: Vec<String>,
    registered_inventory: bool,
    from_address: String,
    to_address: String,
    amount: u64,
    fee: u64,
    memo: String,
    observed_height: Option<u64>,
    status: String,
}

#[derive(Debug, Clone)]
struct ExpectedVaultReplay {
    address: String,
    birthday: u64,
    inventory_fingerprint: String,
    key_images: Vec<ReplayKeyImageRef>,
    outbounds: Vec<ReplayOutbound>,
}

fn normalize_tx_hash(hash: &str) -> String {
    hash.trim().to_ascii_lowercase()
}

fn normalize_tx_keys(tx_keys: &[String]) -> Vec<String> {
    let mut normalized = tx_keys
        .iter()
        .map(|key| key.trim().to_ascii_lowercase())
        .filter(|key| !key.is_empty())
        .collect::<Vec<_>>();
    normalized.sort();
    normalized
}

fn pub_key_to_group_key_hex(vault_pub_key: &str) -> anyhow::Result<String> {
    let (_hrp, data, _variant) = bech32_decode(vault_pub_key)
        .map_err(|e| anyhow::anyhow!("failed to decode vault pubkey {vault_pub_key}: {e}"))?;
    let decoded = Vec::<u8>::from_base32(&data).map_err(|e| {
        anyhow::anyhow!("failed to decode base32 vault pubkey {vault_pub_key}: {e}")
    })?;
    if !decoded.starts_with(ED25519_PUBKEY_AMINO_PREFIX) {
        anyhow::bail!("vault pubkey {vault_pub_key} has unexpected amino prefix");
    }
    if decoded.len() != ED25519_PUBKEY_AMINO_PREFIX.len() + 32 {
        anyhow::bail!("vault pubkey {vault_pub_key} has unexpected decoded length");
    }
    Ok(hex::encode(&decoded[ED25519_PUBKEY_AMINO_PREFIX.len()..]))
}

fn extract_keys_only(tarball_path: &Path) -> anyhow::Result<(TempDir, PathBuf)> {
    let temp_dir = tempfile::tempdir()?;
    let status = Command::new("tar")
        .arg("-xzf")
        .arg(tarball_path)
        .arg("-C")
        .arg(temp_dir.path())
        .status()?;
    if !status.success() {
        anyhow::bail!("failed to extract {}", tarball_path.display());
    }

    let data_dir = temp_dir.path().join("xmr-frost");
    let sled_dir = data_dir.join("sled_db");
    if sled_dir.exists() {
        std::fs::remove_dir_all(&sled_dir)?;
    }
    Ok((temp_dir, data_dir))
}

fn build_state(data_dir: &Path, daemon_url: &str) -> anyhow::Result<Arc<AppState>> {
    let store = Arc::new(Store::open(data_dir)?);
    let daemon = DaemonClient::new(daemon_url, 8 * 1024 * 1024);
    Ok(Arc::new(AppState {
        dkg: DkgCoordinator::new(store.clone(), ChainNetworkMode::Mainnet, 4),
        signer: SignCoordinator::new(store.clone(), daemon.clone(), ChainNetworkMode::Mainnet, 4),
        key_images: KeyImageCoordinator::new(store.clone(), 4),
        scanner: parking_lot::Mutex::new(Scanner::new()),
        scan_lifecycle: tokio::sync::Mutex::new(()),
        daemon,
        store,
        chain_network: ChainNetworkMode::Mainnet,
        max_sign_scan_lag_blocks: 3,
        sim_helper_enabled: false,
        sim_helper_wallet_rpc_url: None,
    }))
}

fn merge_expected_inventory(
    bundle: &ReplayBundle,
) -> anyhow::Result<HashMap<String, ExpectedVaultReplay>> {
    let asgards_by_vault = bundle
        .asgards
        .iter()
        .map(|asgard| {
            let address = asgard
                .addresses
                .iter()
                .find(|entry| entry.chain == "XMR")
                .ok_or_else(|| {
                    anyhow::anyhow!("missing XMR address for {}", asgard.pub_key_frost)
                })?;
            Ok((
                asgard.pub_key_frost.clone(),
                (
                    address.address.clone(),
                    asgard.vault_creation_height_xmr,
                    asgard.key_images_xmr.clone(),
                    asgard.tx_keys_xmr.clone(),
                ),
            ))
        })
        .collect::<anyhow::Result<HashMap<_, _>>>()?;

    let mut merged_outbounds = HashMap::<String, HashMap<String, ReplayOutbound>>::new();

    for node_state in bundle.node_states.values() {
        for (vault_pub_key, outbounds) in &node_state.outbounds {
            if !asgards_by_vault.contains_key(vault_pub_key) {
                continue;
            }
            let by_hash = merged_outbounds.entry(vault_pub_key.clone()).or_default();
            for outbound in outbounds {
                let tx_hash = normalize_tx_hash(&outbound.tx_hash);
                match by_hash.get_mut(&tx_hash) {
                    Some(existing) => {
                        if normalize_tx_keys(&existing.tx_keys)
                            != normalize_tx_keys(&outbound.tx_keys)
                            || existing.registered_inventory != outbound.registered_inventory
                            || existing.from_address != outbound.from_address
                            || existing.to_address != outbound.to_address
                            || existing.amount != outbound.amount
                            || existing.fee != outbound.fee
                            || existing.memo != outbound.memo
                        {
                            anyhow::bail!(
                                "outbound metadata mismatch across bundle node snapshots for {} {}",
                                vault_pub_key,
                                outbound.tx_hash
                            );
                        }
                        if existing.observed_height.is_none() {
                            existing.observed_height = outbound.observed_height;
                        }
                        if existing.status != "observed_mined"
                            && outbound.status == "observed_mined"
                        {
                            existing.status = outbound.status.clone();
                        } else if existing.status == "attested_pending_observation"
                            && outbound.status == "observed_mempool"
                        {
                            existing.status = outbound.status.clone();
                        }
                    }
                    None => {
                        by_hash.insert(tx_hash, outbound.clone());
                    }
                }
            }
        }
    }

    let mut expected = HashMap::new();
    for (vault_pub_key, (address, birthday, key_images, tx_key_inventory)) in asgards_by_vault {
        let merged = merged_outbounds.remove(&vault_pub_key).unwrap_or_default();
        let mut outbounds = Vec::with_capacity(tx_key_inventory.len());
        for tx_key_entry in tx_key_inventory {
            let tx_hash = normalize_tx_hash(&tx_key_entry.tx_hash);
            let mut outbound = merged.get(&tx_hash).cloned().ok_or_else(|| {
                anyhow::anyhow!(
                    "missing outbound metadata in bundle node state for {} {}",
                    vault_pub_key,
                    tx_hash
                )
            })?;
            outbound.tx_hash = tx_hash;
            outbound.tx_keys = tx_key_entry.tx_keys;
            outbound.registered_inventory = true;
            outbounds.push(outbound);
        }
        for outbound in &mut outbounds {
            outbound.status = if outbound.observed_height.is_some() {
                "observed_mined".to_string()
            } else if outbound.status == "observed_mempool" {
                "observed_mempool".to_string()
            } else {
                "attested_pending_observation".to_string()
            };
        }

        let fingerprint_outbounds = outbounds
            .iter()
            .map(|outbound| OutgoingTransferRecord {
                tx_hash: outbound.tx_hash.clone(),
                tx_hex: String::new(),
                tx_keys: outbound.tx_keys.clone(),
                registered_inventory: true,
                has_owned_change: None,
                local_change_materialized: None,
                key_images_ready: None,
                msg_id: String::new(),
                vault_pub_key: vault_pub_key.clone(),
                from_address: outbound.from_address.clone(),
                to_address: outbound.to_address.clone(),
                amount: outbound.amount,
                fee: outbound.fee,
                memo: outbound.memo.clone(),
                planned_height: outbound.observed_height.unwrap_or_default(),
                observed_height: outbound.observed_height,
                status: if outbound.observed_height.is_some() {
                    OutgoingTransferStatus::ObservedMined
                } else {
                    OutgoingTransferStatus::AttestedPendingObservation
                },
                broadcasted_at: None,
                observed_at: None,
                reported_effective_height: None,
                created_at: 0,
            })
            .collect::<Vec<_>>();
        outbounds.sort_by(|left, right| {
            left.observed_height
                .unwrap_or_default()
                .cmp(&right.observed_height.unwrap_or_default())
                .then_with(|| {
                    normalize_tx_hash(&left.tx_hash).cmp(&normalize_tx_hash(&right.tx_hash))
                })
        });

        expected.insert(
            vault_pub_key.clone(),
            ExpectedVaultReplay {
                address,
                birthday,
                inventory_fingerprint: VaultLifecycle::build_inventory_fingerprint(
                    &vault_pub_key,
                    birthday,
                    &key_images
                        .iter()
                        .map(|entry| crate::state::store::RegisteredKeyImageRef {
                            tx_hash: entry.tx_hash.clone(),
                            output_index: entry.output_index,
                            key_image: entry.key_image.clone(),
                        })
                        .collect::<Vec<_>>(),
                    &fingerprint_outbounds,
                ),
                key_images,
                outbounds,
            },
        );
    }

    Ok(expected)
}

async fn spawn_daemon_server(
    daemon_txs: HashMap<String, serde_json::Value>,
) -> anyhow::Result<String> {
    async fn get_transactions(
        State(responses): State<Arc<HashMap<String, serde_json::Value>>>,
        Json(payload): Json<serde_json::Value>,
    ) -> impl IntoResponse {
        let requested_hashes = payload
            .get("txs_hashes")
            .and_then(|value| value.as_array())
            .cloned()
            .unwrap_or_default();
        let requested_hash = requested_hashes
            .first()
            .and_then(|value| value.as_str())
            .unwrap_or_default()
            .trim()
            .to_string();
        let normalized = normalize_tx_hash(&requested_hash);

        let response = responses
            .get(&requested_hash)
            .or_else(|| responses.get(&normalized))
            .cloned()
            .unwrap_or_else(|| {
                serde_json::json!({
                    "txs": [],
                    "missed_tx": [requested_hash],
                    "status": "OK",
                })
            });

        (StatusCode::OK, Json(response))
    }

    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await?;
    let addr = listener.local_addr()?;
    let app = Router::new()
        .route("/get_transactions", post(get_transactions))
        .with_state(Arc::new(daemon_txs));
    tokio::spawn(async move {
        axum::serve(listener, app)
            .await
            .expect("daemon test server failed");
    });
    Ok(format!("http://{}", addr))
}

async fn response_status_json(
    response: axum::response::Response,
) -> anyhow::Result<(StatusCode, serde_json::Value)> {
    let status = response.status();
    let body = to_bytes(response.into_body(), usize::MAX).await?;
    let json = if body.is_empty() {
        serde_json::json!({})
    } else {
        serde_json::from_slice(&body)?
    };
    Ok((status, json))
}

fn register_request_for_vault(
    vault_pub_key: &str,
    expected: &ExpectedVaultReplay,
) -> anyhow::Result<RegisterVaultRequest> {
    let tx_keys = expected
        .outbounds
        .iter()
        .map(|outbound| RegisterTxKeyEntry {
            tx_hash: outbound.tx_hash.clone(),
            tx_keys: outbound.tx_keys.clone(),
            from_address: Some(outbound.from_address.clone()),
            to_address: outbound.to_address.clone(),
            amount: outbound.amount,
            fee: outbound.fee,
            memo: Some(outbound.memo.clone()),
        })
        .collect::<Vec<_>>();
    let key_images = expected
        .key_images
        .iter()
        .map(|entry| KeyImageEntry {
            tx_hash: entry.tx_hash.clone(),
            output_index: entry.output_index,
            key_image: entry.key_image.clone(),
        })
        .collect::<Vec<_>>();

    Ok(RegisterVaultRequest {
        vault_pub_key: vault_pub_key.to_string(),
        group_key: pub_key_to_group_key_hex(vault_pub_key)?,
        address: expected.address.clone(),
        source: Some("xmr-replay-artifact".to_string()),
        spend_key_hex: None,
        birthday: Some(expected.birthday),
        key_images: Some(key_images),
        tx_keys: Some(tx_keys),
    })
}

fn assert_replayed_outbounds(
    records: Vec<OutgoingTransferRecord>,
    expected: &[ReplayOutbound],
    vault_pub_key: &str,
) {
    assert_eq!(
        records.len(),
        expected.len(),
        "unexpected replayed outbound count for {}",
        vault_pub_key
    );

    let by_hash = records
        .into_iter()
        .map(|record| (normalize_tx_hash(&record.tx_hash), record))
        .collect::<HashMap<_, _>>();

    for expected_outbound in expected {
        let tx_hash = normalize_tx_hash(&expected_outbound.tx_hash);
        let saved = by_hash.get(&tx_hash).unwrap_or_else(|| {
            panic!(
                "missing replayed outbound {} for {}",
                tx_hash, vault_pub_key
            )
        });
        assert_eq!(
            normalize_tx_keys(&saved.tx_keys),
            normalize_tx_keys(&expected_outbound.tx_keys),
            "tx_keys mismatch for {} {}",
            vault_pub_key,
            tx_hash
        );
        assert_eq!(
            saved.registered_inventory,
            expected_outbound.registered_inventory
        );
        assert_eq!(saved.from_address, expected_outbound.from_address);
        assert_eq!(saved.to_address, expected_outbound.to_address);
        assert_eq!(saved.amount, expected_outbound.amount);
        assert_eq!(saved.fee, expected_outbound.fee);
        assert_eq!(saved.memo, expected_outbound.memo);
        assert_eq!(saved.observed_height, expected_outbound.observed_height);
        assert_eq!(saved.status.to_string(), expected_outbound.status);
    }
}

#[tokio::test]
#[ignore = "local artifact replay harness; set XMR_REPLAY_BUNDLE_DIR to run"]
async fn replay_bundle_inventory_rebuilds_signer_state_from_artifact() -> anyhow::Result<()> {
    let bundle_dir = PathBuf::from(
        std::env::var(REPLAY_BUNDLE_DIR_ENV)
            .map_err(|_| anyhow::anyhow!("{REPLAY_BUNDLE_DIR_ENV} is not set"))?,
    );
    let bundle_path = bundle_dir.join("bundle.json");
    let bundle = serde_json::from_slice::<ReplayBundle>(&std::fs::read(&bundle_path)?)?;
    let expected_inventory = merge_expected_inventory(&bundle)?;
    let daemon_url = spawn_daemon_server(bundle.daemon_txs.clone()).await?;

    let mut node_names = bundle.node_states.keys().cloned().collect::<Vec<_>>();
    node_names.sort();

    for node_name in node_names {
        let tarball_path = bundle_dir
            .join("keyshares")
            .join(format!("{node_name}.tar.gz"));
        let (_temp_dir, data_dir) = extract_keys_only(&tarball_path)?;
        let state = build_state(&data_dir, &daemon_url)?;

        let mut vault_pub_keys = expected_inventory.keys().cloned().collect::<Vec<_>>();
        vault_pub_keys.sort();

        for _ in 0..2 {
            for vault_pub_key in &vault_pub_keys {
                let request = register_request_for_vault(
                    vault_pub_key,
                    expected_inventory
                        .get(vault_pub_key)
                        .expect("expected vault replay entry missing"),
                )?;
                let (status, body) = response_status_json(
                    register_vault(State(state.clone()), Json(request))
                        .await
                        .into_response(),
                )
                .await?;
                assert_eq!(
                    status,
                    StatusCode::OK,
                    "register_vault failed for {node_name} {vault_pub_key}: {body}"
                );
            }
        }

        let (status, body) = response_status_json(
            list_vault_states(State(state.clone()))
                .await
                .into_response(),
        )
        .await?;
        assert_eq!(
            status,
            StatusCode::OK,
            "list_vault_states failed for {node_name}: {body}"
        );

        let vaults = state.store.list_vaults()?;
        assert_eq!(
            vaults.len(),
            expected_inventory.len(),
            "unexpected vault count for {node_name}"
        );
        for vault in vaults {
            let expected = expected_inventory
                .get(&vault.vault_pub_key)
                .unwrap_or_else(|| {
                    panic!("unexpected vault {} for {}", vault.vault_pub_key, node_name)
                });
            let snapshot = VaultLifecycle::new(state.store.as_ref()).snapshot(&vault)?;
            assert_eq!(snapshot.address, expected.address);
            assert_eq!(snapshot.birthday, expected.birthday);
            assert_eq!(
                snapshot.registered_key_image_count,
                expected.key_images.len(),
                "registered key image count mismatch for {} {}",
                node_name,
                vault.vault_pub_key
            );
            assert_eq!(
                snapshot.registered_tx_key_count,
                expected.outbounds.len(),
                "registered tx-key count mismatch for {} {}",
                node_name,
                vault.vault_pub_key
            );
            assert_eq!(
                snapshot.inventory_fingerprint, expected.inventory_fingerprint,
                "inventory fingerprint mismatch for {} {}",
                node_name, vault.vault_pub_key
            );

            let outgoing = state.store.load_outgoing_transfers(&vault.vault_pub_key)?;
            assert_replayed_outbounds(outgoing, &expected.outbounds, &vault.vault_pub_key);
        }
    }

    Ok(())
}
