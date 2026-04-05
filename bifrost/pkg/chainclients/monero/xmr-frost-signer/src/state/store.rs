use std::collections::{HashMap, HashSet};
use std::path::{Path, PathBuf};

use anyhow::{Context, Result};
use dalek_ff_group::Ed25519 as FrostEd25519;
use dkg_pedpop::ThresholdKeys as PedPoPThresholdKeys;
use tracing::{info, warn};

const PRIVATE_DIR_MODE: u32 = 0o700;
const PRIVATE_FILE_MODE: u32 = 0o600;
pub const IDEMPOTENCY_RETENTION_SECS: i64 = 86_400;
pub const IDEMPOTENCY_PENDING_RECLAIM_SECS: i64 = 600;
pub const IDEMPOTENCY_PRUNE_INTERVAL_SECS: u64 = 600;

fn is_idempotency_expired(created_at: i64, now_ts: i64) -> bool {
    now_ts
        .checked_sub(created_at)
        .map(|age| age > IDEMPOTENCY_RETENTION_SECS)
        .unwrap_or(true)
}

fn is_idempotency_pending_stale(created_at: i64, now_ts: i64) -> bool {
    now_ts
        .checked_sub(created_at)
        .map(|age| age > IDEMPOTENCY_PENDING_RECLAIM_SECS)
        .unwrap_or(false)
}

pub struct Store {
    db: sled::Db,
    data_dir: PathBuf,
}

/// A registered vault with its group key and scanning metadata.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct VaultRecord {
    pub vault_pub_key: String,
    pub group_key: String,
    pub address: String,
    pub source: String,
    pub registered_at: i64,
}

#[derive(Debug, Clone, Copy, serde::Serialize, serde::Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum OutputState {
    Unspent,
    Reserved,
    Spent,
}

impl Default for OutputState {
    fn default() -> Self {
        OutputState::Unspent
    }
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize, PartialEq, Eq, Hash)]
pub struct OutputRef {
    pub tx_hash: String,
    pub output_index: u32,
}

#[derive(Debug, Clone, Copy, serde::Serialize, serde::Deserialize, PartialEq, Eq, Hash)]
pub struct DepositRoute {
    pub subaddress_major: u32,
    pub subaddress_minor: u32,
}

impl DepositRoute {
    pub fn new(subaddress_major: u32, subaddress_minor: u32) -> Self {
        Self {
            subaddress_major,
            subaddress_minor,
        }
    }
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize, PartialEq, Eq)]
pub struct CollisionRecord {
    pub one_time_key: String,
    pub route: DepositRoute,
    pub canonical_ref: Option<OutputRef>,
    pub credited_amount: u64,
    #[serde(default)]
    pub shadowed_refs: Vec<OutputRef>,
    #[serde(default)]
    pub quarantined_refs: Vec<OutputRef>,
    pub resolved_spent_ref: Option<OutputRef>,
}

impl CollisionRecord {
    pub fn is_resolved(&self) -> bool {
        self.resolved_spent_ref.is_some()
    }

    pub fn contains_ref(&self, output_ref: &OutputRef) -> bool {
        self.canonical_ref
            .as_ref()
            .is_some_and(|current| current == output_ref)
            || self
                .shadowed_refs
                .iter()
                .any(|shadowed| shadowed == output_ref)
            || self
                .quarantined_refs
                .iter()
                .any(|quarantined| quarantined == output_ref)
            || self
                .resolved_spent_ref
                .as_ref()
                .is_some_and(|resolved| resolved == output_ref)
    }
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct OutputReservation {
    pub session_id: String,
    pub msg_id: String,
    pub reserved_at: i64,
    pub expires_at: i64,
    pub tx_hash_candidate: Option<String>,
}

/// A tracked UTXO for a vault.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct TrackedOutput {
    pub tx_hash: String,
    pub output_index: u32,
    pub amount: u64,
    pub height: u64,
    #[serde(default)]
    pub one_time_key: Option<String>,
    pub key_image: Option<String>,
    pub state: OutputState,
    pub reservation: Option<OutputReservation>,
    pub subaddress_major: u32,
    pub subaddress_minor: u32,
    pub wallet_output_hex: Option<String>,
    pub address: Option<String>,
    pub note: Option<String>,
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize, PartialEq, Eq)]
pub struct CreditedOutput {
    pub tx_hash: String,
    pub output_index: u32,
    pub amount: u64,
}

impl TrackedOutput {
    pub const MIN_SPENDABLE_CONFIRMATIONS: u64 = 10;

    pub fn ref_key(&self) -> OutputRef {
        OutputRef {
            tx_hash: self.tx_hash.clone(),
            output_index: self.output_index,
        }
    }

    pub fn validate(&self) -> Result<()> {
        if self.tx_hash.trim().is_empty() {
            anyhow::bail!("tracked output tx_hash must not be empty");
        }
        if self.state == OutputState::Reserved && self.reservation.is_none() {
            anyhow::bail!(
                "tracked output {}:{} is reserved but missing reservation metadata",
                self.tx_hash,
                self.output_index
            );
        }
        if self.state != OutputState::Reserved && self.reservation.is_some() {
            anyhow::bail!(
                "tracked output {}:{} has reservation metadata in non-reserved state",
                self.tx_hash,
                self.output_index
            );
        }
        if self
            .wallet_output_hex
            .as_ref()
            .map(|hex| hex.trim().is_empty())
            .unwrap_or(true)
        {
            anyhow::bail!(
                "tracked output {}:{} is missing wallet_output_hex",
                self.tx_hash,
                self.output_index
            );
        }
        Ok(())
    }

    pub fn is_available_for_spend(&self) -> bool {
        self.state == OutputState::Unspent
    }

    pub fn confirmations_at(&self, current_height: u64) -> u64 {
        current_height.saturating_sub(self.height)
    }

    pub fn is_unlocked_at(&self, current_height: u64) -> bool {
        self.confirmations_at(current_height) >= Self::MIN_SPENDABLE_CONFIRMATIONS
    }

    pub fn is_available_for_spend_at(&self, current_height: u64) -> bool {
        self.is_available_for_spend() && self.is_unlocked_at(current_height)
    }

    pub fn is_reserved(&self) -> bool {
        self.state == OutputState::Reserved
    }

    pub fn is_spent(&self) -> bool {
        self.state == OutputState::Spent
    }
}

fn compare_collision_candidates(lhs: &TrackedOutput, rhs: &TrackedOutput) -> std::cmp::Ordering {
    rhs.amount
        .cmp(&lhs.amount)
        .then_with(|| lhs.height.cmp(&rhs.height))
        .then_with(|| lhs.tx_hash.cmp(&rhs.tx_hash))
        .then_with(|| lhs.output_index.cmp(&rhs.output_index))
}

fn push_unique_ref(refs: &mut Vec<OutputRef>, output_ref: OutputRef) {
    if refs.iter().any(|existing| existing == &output_ref) {
        return;
    }
    refs.push(output_ref);
}

fn output_route(output: &TrackedOutput) -> DepositRoute {
    DepositRoute::new(output.subaddress_major, output.subaddress_minor)
}

fn output_ref_key(output: &TrackedOutput) -> OutputRef {
    output.ref_key()
}

fn outputs_by_ref(outputs: &[TrackedOutput]) -> HashMap<(String, u32), &TrackedOutput> {
    outputs
        .iter()
        .map(|output| ((output.tx_hash.clone(), output.output_index), output))
        .collect()
}

fn output_lookup<'a>(
    by_ref: &'a HashMap<(String, u32), &'a TrackedOutput>,
    output_ref: &OutputRef,
) -> Option<&'a TrackedOutput> {
    by_ref
        .get(&(output_ref.tx_hash.clone(), output_ref.output_index))
        .copied()
}

fn adopt_canonical_ref(
    record: &mut CollisionRecord,
    outputs: &[TrackedOutput],
    output_ref: &OutputRef,
) {
    if let Some(previous) = record.canonical_ref.replace(output_ref.clone()) {
        if previous != *output_ref {
            push_unique_ref(&mut record.shadowed_refs, previous);
        }
    }
    if let Some(output) = outputs
        .iter()
        .find(|candidate| candidate.ref_key() == *output_ref)
    {
        record.credited_amount = output.amount;
    }
}

pub fn sync_collision_records_after_output_state_change(
    records: &mut Vec<CollisionRecord>,
    outputs: &[TrackedOutput],
) {
    let by_ref = outputs_by_ref(outputs);
    records.retain_mut(|record| {
        record.shadowed_refs.retain(|output_ref| {
            output_lookup(&by_ref, output_ref)
                .is_some_and(|output| output_route(output) == record.route)
        });
        record
            .quarantined_refs
            .retain(|output_ref| output_lookup(&by_ref, output_ref).is_some());

        if let Some(canonical_ref) = record.canonical_ref.clone() {
            let canonical_valid = output_lookup(&by_ref, &canonical_ref)
                .is_some_and(|output| output_route(output) == record.route);
            if !canonical_valid {
                record.canonical_ref = None;
            }
        }

        let spent_candidate = record
            .canonical_ref
            .iter()
            .chain(record.shadowed_refs.iter())
            .chain(record.quarantined_refs.iter())
            .find_map(|output_ref| {
                output_lookup(&by_ref, output_ref)
                    .filter(|output| output.is_spent())
                    .map(|_| output_ref.clone())
            });

        match spent_candidate {
            Some(spent_ref) => {
                record.resolved_spent_ref = Some(spent_ref);
            }
            None => {
                record.resolved_spent_ref = None;
            }
        }

        if record.canonical_ref.is_none() {
            let mut same_route_candidates = record
                .shadowed_refs
                .iter()
                .filter_map(|output_ref| {
                    output_lookup(&by_ref, output_ref).map(|output| (output_ref, output))
                })
                .filter(|(_, output)| output_route(output) == record.route)
                .collect::<Vec<_>>();
            same_route_candidates
                .sort_by(|(_, lhs), (_, rhs)| compare_collision_candidates(lhs, rhs));
            if let Some((winner_ref, _)) = same_route_candidates.first() {
                let winner_ref = (*winner_ref).clone();
                record
                    .shadowed_refs
                    .retain(|output_ref| output_ref != &winner_ref);
                adopt_canonical_ref(record, outputs, &winner_ref);
            } else {
                record.credited_amount = 0;
            }
        } else if let Some(canonical_ref) = record.canonical_ref.as_ref() {
            if let Some(output) = output_lookup(&by_ref, canonical_ref) {
                record.credited_amount = output.amount;
            } else {
                record.credited_amount = 0;
            }
        }

        record.canonical_ref.is_some()
            || !record.shadowed_refs.is_empty()
            || !record.quarantined_refs.is_empty()
            || record.resolved_spent_ref.is_some()
    });
}

pub fn rebuild_collision_records(outputs: &[TrackedOutput]) -> Vec<CollisionRecord> {
    let mut sorted = outputs.iter().cloned().collect::<Vec<_>>();
    sorted.sort_by(|lhs, rhs| {
        lhs.height
            .cmp(&rhs.height)
            .then_with(|| lhs.tx_hash.cmp(&rhs.tx_hash))
            .then_with(|| lhs.output_index.cmp(&rhs.output_index))
    });

    let mut records = Vec::<CollisionRecord>::new();
    for output in sorted {
        let Some(one_time_key) = output.one_time_key.clone() else {
            continue;
        };
        let output_ref = output_ref_key(&output);
        let route = output_route(&output);
        if let Some(record) = records
            .iter_mut()
            .find(|record| record.one_time_key == one_time_key)
        {
            if record.route != route {
                push_unique_ref(&mut record.quarantined_refs, output_ref);
                continue;
            }
            if record.is_resolved() {
                push_unique_ref(&mut record.shadowed_refs, output_ref);
                continue;
            }
            let current = record.canonical_ref.as_ref().and_then(|canonical_ref| {
                outputs
                    .iter()
                    .find(|candidate| candidate.ref_key() == *canonical_ref)
            });
            if current.is_none() {
                adopt_canonical_ref(record, outputs, &output_ref);
                continue;
            }
            if compare_collision_candidates(&output, current.expect("checked is_some"))
                == std::cmp::Ordering::Less
            {
                adopt_canonical_ref(record, outputs, &output_ref);
                continue;
            }
            push_unique_ref(&mut record.shadowed_refs, output_ref);
            continue;
        }

        records.push(CollisionRecord {
            one_time_key,
            route,
            canonical_ref: Some(output_ref),
            credited_amount: output.amount,
            shadowed_refs: Vec::new(),
            quarantined_refs: Vec::new(),
            resolved_spent_ref: None,
        });
    }

    sync_collision_records_after_output_state_change(&mut records, outputs);
    records
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct OutgoingTransferRecord {
    pub tx_hash: String,
    pub tx_hex: String,
    pub msg_id: String,
    pub vault_pub_key: String,
    pub from_address: String,
    pub to_address: String,
    pub amount: u64,
    pub fee: u64,
    pub memo: String,
    pub planned_height: u64,
    pub observed_height: Option<u64>,
    pub status: String,
    pub broadcasted_at: Option<i64>,
    pub observed_at: Option<i64>,
    pub created_at: i64,
}

fn enforce_private_dir_permissions(path: &Path) -> Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(PRIVATE_DIR_MODE))
            .with_context(|| format!("failed to set 0700 permissions on {}", path.display()))?;
    }
    Ok(())
}

fn enforce_private_file_permissions(path: &Path) -> Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(PRIVATE_FILE_MODE))
            .with_context(|| format!("failed to set 0600 permissions on {}", path.display()))?;
    }
    Ok(())
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct IdempotencyRecord {
    pub route: String,
    pub msg_id: String,
    pub payload_hash: String,
    pub response_hash: String,
    pub status_code: u16,
    pub response_json: serde_json::Value,
    pub created_at: i64,
    pub state: IdempotencyState,
}

#[derive(Debug, Clone, Copy, serde::Serialize, serde::Deserialize, PartialEq, Eq)]
#[serde(rename_all = "snake_case")]
pub enum IdempotencyState {
    Pending,
    Complete,
}

impl Default for IdempotencyState {
    fn default() -> Self {
        IdempotencyState::Complete
    }
}

#[derive(Debug, Clone)]
pub enum IdempotencyClaimResult {
    Fresh,
    Replay(IdempotencyRecord),
    Conflict,
    InProgress,
}

#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct FinalizeReservedForMsgResult {
    pub finalized: usize,
    pub missing_candidate: usize,
    pub candidate_mismatch: usize,
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct ScanBlockMeta {
    pub height: u64,
    pub block_hash: String,
    pub parent_hash: String,
    pub added_outputs: Vec<OutputRef>,
    #[serde(default)]
    pub credited_outputs: Vec<CreditedOutput>,
}

#[derive(Debug, Clone, serde::Serialize, serde::Deserialize, Default)]
pub struct VaultTrackedState {
    #[serde(default)]
    pub outputs: Vec<TrackedOutput>,
    #[serde(default)]
    pub collision_records: Vec<CollisionRecord>,
}

impl Store {
    pub fn open(data_dir: &Path) -> Result<Self> {
        std::fs::create_dir_all(data_dir)
            .with_context(|| format!("failed to create data dir: {}", data_dir.display()))?;
        enforce_private_dir_permissions(data_dir)?;

        let db_path = data_dir.join("sled_db");
        let db = sled::open(&db_path)
            .with_context(|| format!("failed to open sled DB at {}", db_path.display()))?;

        let store = Store {
            db,
            data_dir: data_dir.to_path_buf(),
        };
        let verified_key_files = store.verify_key_files_on_startup()?;

        info!(
            path = %data_dir.display(),
            verified_key_files,
            "store opened"
        );

        Ok(store)
    }

    fn list_key_file_paths(data_dir: &Path) -> Result<Vec<PathBuf>> {
        let keys_dir = data_dir.join("keys");
        if !keys_dir.exists() {
            return Ok(Vec::new());
        }
        let mut paths = Vec::new();
        for entry in std::fs::read_dir(&keys_dir)
            .with_context(|| format!("failed to list key directory: {}", keys_dir.display()))?
        {
            let entry = entry?;
            let path = entry.path();
            if !path.is_file() {
                continue;
            }
            if path
                .extension()
                .map(|ext| ext.to_string_lossy().eq_ignore_ascii_case("frost"))
                .unwrap_or(false)
            {
                paths.push(path);
            }
        }
        paths.sort();
        Ok(paths)
    }

    fn write_key_file_atomic(path: &Path, encrypted: &[u8]) -> Result<()> {
        let file_name = path
            .file_name()
            .ok_or_else(|| anyhow::anyhow!("missing key file name for {}", path.display()))?;
        let tmp_path = path.with_file_name(format!("{}.tmp", file_name.to_string_lossy()));
        std::fs::write(&tmp_path, encrypted)
            .with_context(|| format!("failed to write tmp key file: {}", tmp_path.display()))?;
        enforce_private_file_permissions(&tmp_path)?;
        std::fs::rename(&tmp_path, path).with_context(|| {
            format!("failed to atomically replace key file: {}", path.display())
        })?;
        enforce_private_file_permissions(path)?;
        Ok(())
    }

    fn validate_threshold_key_payload(data: &[u8]) -> Result<()> {
        let mut cursor = data;
        PedPoPThresholdKeys::<FrostEd25519>::read(&mut cursor)
            .map_err(|e| anyhow::anyhow!("invalid threshold key payload: {}", e))?;
        if !cursor.is_empty() {
            anyhow::bail!("invalid threshold key payload: trailing bytes detected");
        }
        Ok(())
    }

    fn verify_key_files_on_startup(&self) -> Result<usize> {
        let key_paths = Self::list_key_file_paths(&self.data_dir)?;
        for path in &key_paths {
            let data = std::fs::read(path)
                .with_context(|| format!("failed to read key file: {}", path.display()))?;
            Self::validate_threshold_key_payload(&data).with_context(|| {
                format!(
                    "invalid threshold key material in key file: {}",
                    path.display()
                )
            })?;
        }
        Ok(key_paths.len())
    }

    // ---- FROST key persistence ----

    pub fn save_frost_keys(&self, vault_pub_key: &str, serialized_keys: &[u8]) -> Result<()> {
        let keys_dir = self.data_dir.join("keys");
        std::fs::create_dir_all(&keys_dir)?;
        enforce_private_dir_permissions(&keys_dir)?;

        let final_path = keys_dir.join(format!("{}.frost", vault_pub_key));
        Self::write_key_file_atomic(&final_path, serialized_keys)?;

        info!(vault = vault_pub_key, "saved FROST keys");
        Ok(())
    }

    pub fn load_frost_keys(&self, vault_pub_key: &str) -> Result<Option<Vec<u8>>> {
        let path = self
            .data_dir
            .join("keys")
            .join(format!("{}.frost", vault_pub_key));
        if !path.exists() {
            return Ok(None);
        }

        let data = std::fs::read(&path)
            .with_context(|| format!("failed to read key file: {}", path.display()))?;
        Ok(Some(data))
    }

    // ---- Vault registry ----

    /// Register a vault with immutable mapping semantics.
    /// Returns true when a new record was created, false when same mapping already existed.
    pub fn register_vault(
        &self,
        vault_pub_key: &str,
        group_key: &str,
        address: &str,
        source: &str,
    ) -> Result<bool> {
        let tree = self.db.open_tree("vaults")?;
        let record = VaultRecord {
            vault_pub_key: vault_pub_key.to_string(),
            group_key: group_key.to_string(),
            address: address.to_string(),
            source: source.to_string(),
            registered_at: chrono::Utc::now().timestamp(),
        };
        let value = serde_json::to_vec(&record)?;
        let key = vault_pub_key.as_bytes();

        match tree.compare_and_swap(key, None as Option<&[u8]>, Some(value.as_slice())) {
            Ok(Ok(())) => {
                tree.flush()?;
                info!(vault = vault_pub_key, "registered vault");
                Ok(true)
            }
            Ok(Err(existing)) => {
                let Some(current) = existing.current.as_ref() else {
                    anyhow::bail!(
                        "vault registration compare-and-swap conflict without current value"
                    );
                };
                let existing_record: VaultRecord = serde_json::from_slice(current)?;
                let same_mapping =
                    existing_record.group_key == group_key && existing_record.address == address;
                if same_mapping {
                    Ok(false)
                } else {
                    anyhow::bail!(
                        "vault {} is already registered with a different mapping",
                        vault_pub_key
                    )
                }
            }
            Err(e) => Err(anyhow::anyhow!(
                "failed to register vault atomically: {}",
                e
            )),
        }
    }

    pub fn remove_vault_if_mapping_matches(
        &self,
        vault_pub_key: &str,
        group_key: &str,
        address: &str,
    ) -> Result<()> {
        let tree = self.db.open_tree("vaults")?;
        let Some(raw) = tree.get(vault_pub_key.as_bytes())? else {
            return Ok(());
        };
        let existing: VaultRecord = serde_json::from_slice(&raw)?;
        if existing.group_key == group_key && existing.address == address {
            tree.remove(vault_pub_key.as_bytes())?;
            tree.flush()?;
        }
        Ok(())
    }

    /// Get a registered vault.
    pub fn get_vault(&self, vault_pub_key: &str) -> Result<Option<VaultRecord>> {
        let tree = self.db.open_tree("vaults")?;
        match tree.get(vault_pub_key.as_bytes())? {
            Some(bytes) => {
                let record: VaultRecord = serde_json::from_slice(&bytes)?;
                Ok(Some(record))
            }
            None => Ok(None),
        }
    }

    /// List all registered vaults.
    pub fn list_vaults(&self) -> Result<Vec<VaultRecord>> {
        let tree = self.db.open_tree("vaults")?;
        let mut vaults = Vec::new();
        for item in tree.iter() {
            let (_, value) = item?;
            let record: VaultRecord = serde_json::from_slice(&value)?;
            vaults.push(record);
        }
        Ok(vaults)
    }

    // ---- UTXO tracking ----

    fn decode_vault_tracked_state(raw: Option<&[u8]>) -> Result<VaultTrackedState> {
        match raw {
            Some(bytes) => {
                let state: VaultTrackedState = serde_json::from_slice(bytes)?;
                for output in &state.outputs {
                    output.validate()?;
                }
                Ok(state)
            }
            None => Ok(VaultTrackedState::default()),
        }
    }

    fn cas_update_vault_tracked_state<T, F>(&self, vault_pub_key: &str, mut mutate: F) -> Result<T>
    where
        F: FnMut(&mut VaultTrackedState) -> Result<T>,
    {
        let tree = self.db.open_tree("outputs")?;
        let key = vault_pub_key.as_bytes();

        loop {
            let current = tree.get(key)?;
            let mut state = Self::decode_vault_tracked_state(current.as_deref())?;
            let result = mutate(&mut state)?;
            for output in &state.outputs {
                output.validate()?;
            }
            let value = serde_json::to_vec(&state)?;
            match tree.compare_and_swap(
                key,
                current.as_ref().map(|v| v.as_ref()),
                Some(value.as_slice()),
            ) {
                Ok(Ok(())) => {
                    tree.flush()?;
                    return Ok(result);
                }
                Ok(Err(_)) => continue,
                Err(e) => {
                    return Err(anyhow::anyhow!(
                        "failed to update outputs atomically: {}",
                        e
                    ))
                }
            }
        }
    }

    fn cas_update_outputs<T, F>(&self, vault_pub_key: &str, mut mutate: F) -> Result<T>
    where
        F: FnMut(&mut Vec<TrackedOutput>) -> Result<T>,
    {
        self.cas_update_vault_tracked_state(vault_pub_key, |state| {
            let result = mutate(&mut state.outputs)?;
            sync_collision_records_after_output_state_change(
                &mut state.collision_records,
                &state.outputs,
            );
            Ok(result)
        })
    }

    pub fn save_vault_tracked_state(
        &self,
        vault_pub_key: &str,
        state: &VaultTrackedState,
    ) -> Result<()> {
        let tree = self.db.open_tree("outputs")?;
        for output in &state.outputs {
            output.validate()?;
        }
        let value = serde_json::to_vec(state)?;
        tree.insert(vault_pub_key.as_bytes(), value)?;
        tree.flush()?;
        Ok(())
    }

    pub fn load_vault_tracked_state(&self, vault_pub_key: &str) -> Result<VaultTrackedState> {
        let tree = self.db.open_tree("outputs")?;
        let current = tree.get(vault_pub_key.as_bytes())?;
        Self::decode_vault_tracked_state(current.as_deref())
    }

    /// Save tracked outputs for a vault.
    pub fn save_outputs(&self, vault_pub_key: &str, outputs: &[TrackedOutput]) -> Result<()> {
        self.save_vault_tracked_state(
            vault_pub_key,
            &VaultTrackedState {
                outputs: outputs.to_vec(),
                collision_records: rebuild_collision_records(outputs),
            },
        )
    }

    /// Merge scanner-produced outputs into the store, preserving any
    /// reservation or spent state set by the signing flow.  Uses CAS to
    /// prevent a concurrent scanner write from clobbering in-flight
    /// output reservations.
    pub fn merge_scanned_scan_state(
        &self,
        vault_pub_key: &str,
        scanned: &[TrackedOutput],
        collision_records: &[CollisionRecord],
    ) -> Result<()> {
        self.cas_update_vault_tracked_state(vault_pub_key, |existing| {
            // Index existing outputs whose lifecycle state must survive the
            // scanner persistence (reserved or spent).
            let mut protected: HashMap<(String, u32), (OutputState, Option<OutputReservation>)> =
                HashMap::new();
            for output in existing.outputs.iter() {
                if output.state == OutputState::Reserved || output.state == OutputState::Spent {
                    protected.insert(
                        (output.tx_hash.clone(), output.output_index),
                        (output.state, output.reservation.clone()),
                    );
                }
            }

            // Replace the output list with the scanned set, re-applying any
            // protected reservation/spent state from the store.
            existing.outputs.clear();
            for scanned_output in scanned {
                let mut output = scanned_output.clone();
                let key = (output.tx_hash.clone(), output.output_index);
                if let Some((state, reservation)) = protected.get(&key) {
                    output.state = *state;
                    output.reservation = reservation.clone();
                }
                existing.outputs.push(output);
            }
            existing.collision_records = collision_records.to_vec();
            sync_collision_records_after_output_state_change(
                &mut existing.collision_records,
                &existing.outputs,
            );
            Ok(())
        })
    }

    /// Load tracked outputs for a vault.
    pub fn load_outputs(&self, vault_pub_key: &str) -> Result<Vec<TrackedOutput>> {
        Ok(self.load_vault_tracked_state(vault_pub_key)?.outputs)
    }

    pub fn save_outgoing_transfer(&self, record: &OutgoingTransferRecord) -> Result<()> {
        if record.msg_id.trim().is_empty() {
            anyhow::bail!("outgoing transfer msg_id must not be empty");
        }
        if record.status.trim().is_empty() {
            anyhow::bail!("outgoing transfer status must not be empty");
        }
        let tree = self.db.open_tree("outgoing_transfers")?;
        let key = format!("{}:{}", record.vault_pub_key, record.tx_hash);
        let value = serde_json::to_vec(record)?;
        tree.insert(key.as_bytes(), value)?;
        tree.flush()?;
        Ok(())
    }

    pub fn load_outgoing_transfers(
        &self,
        vault_pub_key: &str,
    ) -> Result<Vec<OutgoingTransferRecord>> {
        let tree = self.db.open_tree("outgoing_transfers")?;
        let mut records: Vec<OutgoingTransferRecord> = Vec::new();
        let prefix = format!("{}:", vault_pub_key);
        for item in tree.scan_prefix(prefix.as_bytes()) {
            let (_, value) = item?;
            records.push(serde_json::from_slice(&value)?);
        }
        records.sort_by_key(|r| r.created_at);
        Ok(records)
    }

    pub fn load_outgoing_transfer(
        &self,
        vault_pub_key: &str,
        tx_hash: &str,
    ) -> Result<Option<OutgoingTransferRecord>> {
        let tree = self.db.open_tree("outgoing_transfers")?;
        let key = format!("{}:{}", vault_pub_key, tx_hash);
        let Some(raw) = tree.get(key.as_bytes())? else {
            return Ok(None);
        };
        let record: OutgoingTransferRecord = serde_json::from_slice(&raw)?;
        Ok(Some(record))
    }

    pub fn update_outgoing_transfer_status(
        &self,
        vault_pub_key: &str,
        tx_hash: &str,
        status: &str,
        observed_height: Option<u64>,
        observed_at: Option<i64>,
        broadcasted_at: Option<i64>,
    ) -> Result<()> {
        let tree = self.db.open_tree("outgoing_transfers")?;
        let key = format!("{}:{}", vault_pub_key, tx_hash);
        let Some(raw) = tree.get(key.as_bytes())? else {
            return Ok(());
        };
        let mut record: OutgoingTransferRecord = serde_json::from_slice(&raw)?;
        record.status = status.to_string();
        if observed_height.is_some() {
            record.observed_height = observed_height;
        }
        if observed_at.is_some() {
            record.observed_at = observed_at;
        }
        if broadcasted_at.is_some() {
            record.broadcasted_at = broadcasted_at;
        }
        tree.insert(key.as_bytes(), serde_json::to_vec(&record)?)?;
        tree.flush()?;
        Ok(())
    }

    // ---- Output reservation lifecycle ----

    pub fn reserve_outputs(
        &self,
        vault_pub_key: &str,
        selected: &[OutputRef],
        session_id: &str,
        msg_id: &str,
        expires_at: i64,
    ) -> Result<()> {
        let selected_map: HashSet<(String, u32)> = selected
            .iter()
            .map(|r| (r.tx_hash.clone(), r.output_index))
            .collect();

        self.cas_update_outputs(vault_pub_key, |outputs| {
            let mut conflicted = Vec::new();
            let mut seen_selected: HashSet<(String, u32)> = HashSet::new();
            let now_ts = chrono::Utc::now().timestamp();

            for output in outputs.iter_mut() {
                let key = (output.tx_hash.clone(), output.output_index);
                if !selected_map.contains(&key) {
                    continue;
                }
                seen_selected.insert(key.clone());
                match output.state {
                    OutputState::Unspent => {
                        output.state = OutputState::Reserved;
                        output.reservation = Some(OutputReservation {
                            session_id: session_id.to_string(),
                            msg_id: msg_id.to_string(),
                            reserved_at: now_ts,
                            expires_at,
                            tx_hash_candidate: None,
                        });
                    }
                    OutputState::Reserved => {
                        let ours = output
                            .reservation
                            .as_ref()
                            .map(|r| r.session_id == session_id && r.msg_id == msg_id)
                            .unwrap_or(false);
                        if !ours {
                            conflicted.push(output.ref_key());
                        }
                    }
                    OutputState::Spent => conflicted.push(output.ref_key()),
                }
            }

            if seen_selected.len() != selected_map.len() {
                for (tx_hash, output_index) in selected_map.iter() {
                    if seen_selected.contains(&(tx_hash.clone(), *output_index)) {
                        continue;
                    }
                    conflicted.push(OutputRef {
                        tx_hash: tx_hash.clone(),
                        output_index: *output_index,
                    });
                }
            }

            if !conflicted.is_empty() {
                let refs = conflicted
                    .iter()
                    .map(|r| format!("{}:{}", r.tx_hash, r.output_index))
                    .collect::<Vec<_>>()
                    .join(",");
                anyhow::bail!("reservation conflict for outputs: {refs}");
            }
            Ok(())
        })
    }

    pub fn bind_reservation_tx_hash(
        &self,
        vault_pub_key: &str,
        session_id: &str,
        msg_id: &str,
        tx_hash: &str,
    ) -> Result<()> {
        self.cas_update_outputs(vault_pub_key, |outputs| {
            for output in outputs.iter_mut() {
                if output.state != OutputState::Reserved {
                    continue;
                }
                let Some(reservation) = output.reservation.as_mut() else {
                    continue;
                };
                if reservation.session_id == session_id && reservation.msg_id == msg_id {
                    reservation.tx_hash_candidate = Some(tx_hash.to_string());
                }
            }
            Ok(())
        })
    }

    pub fn release_reservations_for_session(
        &self,
        vault_pub_key: &str,
        session_id: &str,
        msg_id: &str,
    ) -> Result<usize> {
        let vault_pub_key = vault_pub_key.trim();
        let session_id = session_id.trim();
        let msg_id = msg_id.trim();
        if vault_pub_key.is_empty() {
            anyhow::bail!("vault_pub_key is required to release reservations");
        }
        if session_id.is_empty() {
            anyhow::bail!("session_id is required to release reservations");
        }
        if msg_id.is_empty() {
            anyhow::bail!("msg_id is required to release reservations");
        }

        self.cas_update_outputs(vault_pub_key, |outputs| {
            let mut released = 0usize;
            for output in outputs.iter_mut() {
                if output.state != OutputState::Reserved {
                    continue;
                }
                let should_release = output.reservation.as_ref().is_some_and(|reservation| {
                    reservation.session_id == session_id && reservation.msg_id == msg_id
                });
                if !should_release {
                    continue;
                }
                output.state = OutputState::Unspent;
                output.reservation = None;
                released += 1;
            }
            Ok(released)
        })
    }

    pub fn finalize_reserved_for_msg(
        &self,
        vault_pub_key: &str,
        msg_id: &str,
        tx_hash: &str,
    ) -> Result<FinalizeReservedForMsgResult> {
        let tx_hash = tx_hash.trim();
        if tx_hash.is_empty() {
            anyhow::bail!("tx_hash is required to finalize reserved outputs");
        }
        self.cas_update_outputs(vault_pub_key, |outputs| {
            let mut result = FinalizeReservedForMsgResult::default();
            for output in outputs.iter_mut() {
                if output.state != OutputState::Reserved {
                    continue;
                }
                let Some(reservation) = output.reservation.as_ref() else {
                    continue;
                };
                if reservation.msg_id != msg_id {
                    continue;
                }
                match reservation.tx_hash_candidate.as_ref() {
                    Some(candidate) if candidate.eq_ignore_ascii_case(tx_hash) => {
                        output.state = OutputState::Spent;
                        output.reservation = None;
                        result.finalized += 1;
                    }
                    Some(_) => {
                        result.candidate_mismatch += 1;
                    }
                    None => {
                        result.missing_candidate += 1;
                    }
                }
            }
            Ok(result)
        })
    }

    pub fn reconcile_reservations(
        &self,
        vault_pub_key: &str,
        observed_hashes: &HashSet<String>,
        now_ts: i64,
    ) -> Result<(usize, usize)> {
        self.cas_update_outputs(vault_pub_key, |outputs| {
            let mut finalized = 0usize;
            let mut released = 0usize;
            for output in outputs.iter_mut() {
                if output.state != OutputState::Reserved {
                    continue;
                }
                let Some(reservation) = output.reservation.as_ref() else {
                    continue;
                };
                let observed = reservation
                    .tx_hash_candidate
                    .as_ref()
                    .map(|hash| observed_hashes.contains(&hash.to_ascii_lowercase()))
                    .unwrap_or(false);
                if observed {
                    output.state = OutputState::Spent;
                    output.reservation = None;
                    finalized += 1;
                    continue;
                }
                if reservation.expires_at <= now_ts {
                    output.state = OutputState::Unspent;
                    output.reservation = None;
                    released += 1;
                }
            }
            Ok((finalized, released))
        })
    }

    pub fn collect_reserved_tx_hashes(&self, vault_pub_key: &str) -> Result<HashSet<String>> {
        let outputs = self.load_outputs(vault_pub_key)?;
        let mut hashes = HashSet::new();
        for output in outputs {
            if output.state != OutputState::Reserved {
                continue;
            }
            if let Some(reservation) = output.reservation {
                if let Some(hash) = reservation.tx_hash_candidate {
                    if !hash.trim().is_empty() {
                        hashes.insert(hash.to_ascii_lowercase());
                    }
                }
            }
        }
        Ok(hashes)
    }

    // ---- Idempotency ----

    pub fn get_idempotency(
        &self,
        route: &str,
        msg_id: &str,
        now_ts: i64,
    ) -> Result<Option<IdempotencyRecord>> {
        let tree = self.db.open_tree("idempotency")?;
        let key = format!("{}:{}", route, msg_id);
        let key_bytes = key.as_bytes();

        loop {
            let Some(raw) = tree.get(key_bytes)? else {
                return Ok(None);
            };
            let record: IdempotencyRecord = serde_json::from_slice(&raw)?;
            if !is_idempotency_expired(record.created_at, now_ts) {
                return Ok(Some(record));
            }

            match tree.compare_and_swap(key_bytes, Some(raw.as_ref()), None as Option<&[u8]>) {
                Ok(Ok(())) => {
                    tree.flush()?;
                    return Ok(None);
                }
                Ok(Err(_)) => continue,
                Err(e) => {
                    return Err(anyhow::anyhow!(
                        "failed to expire idempotency record atomically: {}",
                        e
                    ))
                }
            }
        }
    }

    pub fn save_idempotency(&self, record: &IdempotencyRecord) -> Result<()> {
        let tree = self.db.open_tree("idempotency")?;
        let key = format!("{}:{}", record.route, record.msg_id);
        tree.insert(key.as_bytes(), serde_json::to_vec(record)?)?;
        tree.flush()?;
        Ok(())
    }

    pub fn claim_idempotency(
        &self,
        route: &str,
        msg_id: &str,
        payload_hash: &str,
        now_ts: i64,
    ) -> Result<IdempotencyClaimResult> {
        let tree = self.db.open_tree("idempotency")?;
        let key = format!("{}:{}", route, msg_id);
        let key_bytes = key.as_bytes();
        let payload_hash = payload_hash.trim();
        if payload_hash.is_empty() {
            anyhow::bail!("idempotency payload hash must not be empty");
        }

        loop {
            let current = tree.get(key_bytes)?;
            let Some(raw) = current.as_ref() else {
                let pending = IdempotencyRecord {
                    route: route.to_string(),
                    msg_id: msg_id.to_string(),
                    payload_hash: payload_hash.to_string(),
                    response_hash: String::new(),
                    status_code: 0,
                    response_json: serde_json::Value::Null,
                    created_at: now_ts,
                    state: IdempotencyState::Pending,
                };
                let value = serde_json::to_vec(&pending)?;
                match tree.compare_and_swap(
                    key_bytes,
                    None as Option<&[u8]>,
                    Some(value.as_slice()),
                ) {
                    Ok(Ok(())) => {
                        tree.flush()?;
                        return Ok(IdempotencyClaimResult::Fresh);
                    }
                    Ok(Err(_)) => continue,
                    Err(e) => {
                        return Err(anyhow::anyhow!(
                            "failed to claim idempotency record atomically: {}",
                            e
                        ))
                    }
                }
            };

            let record: IdempotencyRecord = serde_json::from_slice(raw)?;
            if is_idempotency_expired(record.created_at, now_ts) {
                match tree.compare_and_swap(key_bytes, Some(raw.as_ref()), None as Option<&[u8]>) {
                    Ok(Ok(())) => {
                        tree.flush()?;
                        continue;
                    }
                    Ok(Err(_)) => continue,
                    Err(e) => {
                        return Err(anyhow::anyhow!(
                            "failed to expire idempotency record atomically: {}",
                            e
                        ))
                    }
                }
            }

            if record.state == IdempotencyState::Pending
                && is_idempotency_pending_stale(record.created_at, now_ts)
            {
                match tree.compare_and_swap(key_bytes, Some(raw.as_ref()), None as Option<&[u8]>) {
                    Ok(Ok(())) => {
                        warn!(
                            route,
                            msg_id,
                            created_at = record.created_at,
                            now_ts,
                            age_secs = now_ts.saturating_sub(record.created_at),
                            reclaim_after_secs = IDEMPOTENCY_PENDING_RECLAIM_SECS,
                            "reclaimed stale pending idempotency record"
                        );
                        tree.flush()?;
                        continue;
                    }
                    Ok(Err(_)) => continue,
                    Err(e) => {
                        return Err(anyhow::anyhow!(
                            "failed to reclaim stale pending idempotency record atomically: {}",
                            e
                        ))
                    }
                }
            }

            if record.payload_hash != payload_hash {
                return Ok(IdempotencyClaimResult::Conflict);
            }

            if record.state == IdempotencyState::Pending {
                return Ok(IdempotencyClaimResult::InProgress);
            }
            return Ok(IdempotencyClaimResult::Replay(record));
        }
    }

    pub fn clear_idempotency(&self, route: &str, msg_id: &str) -> Result<()> {
        let tree = self.db.open_tree("idempotency")?;
        let key = format!("{}:{}", route, msg_id);
        tree.remove(key.as_bytes())?;
        tree.flush()?;
        Ok(())
    }

    pub fn complete_idempotency(
        &self,
        route: &str,
        msg_id: &str,
        payload_hash: &str,
        response_hash: &str,
        status_code: u16,
        response_json: &serde_json::Value,
    ) -> Result<()> {
        let tree = self.db.open_tree("idempotency")?;
        let key = format!("{}:{}", route, msg_id);
        let key_bytes = key.as_bytes();
        let payload_hash = payload_hash.trim();

        loop {
            let Some(raw) = tree.get(key_bytes)? else {
                anyhow::bail!("idempotency record missing for completion");
            };
            let mut record: IdempotencyRecord = serde_json::from_slice(&raw)?;
            if record.payload_hash != payload_hash {
                anyhow::bail!("idempotency payload hash mismatch during completion");
            }
            if record.state == IdempotencyState::Complete {
                return Ok(());
            }

            record.response_hash = response_hash.to_string();
            record.status_code = status_code;
            record.response_json = response_json.clone();
            record.state = IdempotencyState::Complete;
            let value = serde_json::to_vec(&record)?;
            match tree.compare_and_swap(key_bytes, Some(raw.as_ref()), Some(value.as_slice())) {
                Ok(Ok(())) => {
                    tree.flush()?;
                    return Ok(());
                }
                Ok(Err(_)) => continue,
                Err(e) => {
                    return Err(anyhow::anyhow!(
                        "failed to complete idempotency record atomically: {}",
                        e
                    ))
                }
            }
        }
    }

    pub fn release_pending_idempotency(
        &self,
        route: &str,
        msg_id: &str,
        payload_hash: &str,
    ) -> Result<()> {
        let tree = self.db.open_tree("idempotency")?;
        let key = format!("{}:{}", route, msg_id);
        let key_bytes = key.as_bytes();
        let payload_hash = payload_hash.trim();

        loop {
            let Some(raw) = tree.get(key_bytes)? else {
                return Ok(());
            };
            let record: IdempotencyRecord = serde_json::from_slice(&raw)?;
            if record.payload_hash != payload_hash || record.state != IdempotencyState::Pending {
                return Ok(());
            }
            match tree.compare_and_swap(key_bytes, Some(raw.as_ref()), None as Option<&[u8]>) {
                Ok(Ok(())) => {
                    tree.flush()?;
                    return Ok(());
                }
                Ok(Err(_)) => continue,
                Err(e) => {
                    return Err(anyhow::anyhow!(
                        "failed to release pending idempotency record atomically: {}",
                        e
                    ))
                }
            }
        }
    }

    pub fn prune_expired_idempotency(&self, now_ts: i64) -> Result<usize> {
        let tree = self.db.open_tree("idempotency")?;
        let mut removed = 0usize;

        for item in tree.iter() {
            let (key, raw) = item?;
            let record: IdempotencyRecord = match serde_json::from_slice(&raw) {
                Ok(record) => record,
                Err(e) => {
                    warn!(
                        key = %String::from_utf8_lossy(&key),
                        error = %e,
                        "failed to decode idempotency record during prune"
                    );
                    continue;
                }
            };
            let expired = is_idempotency_expired(record.created_at, now_ts);
            let stale_pending = record.state == IdempotencyState::Pending
                && is_idempotency_pending_stale(record.created_at, now_ts);
            if !expired && !stale_pending {
                continue;
            }
            if stale_pending {
                warn!(
                    route = %record.route,
                    msg_id = %record.msg_id,
                    created_at = record.created_at,
                    now_ts,
                    age_secs = now_ts.saturating_sub(record.created_at),
                    reclaim_after_secs = IDEMPOTENCY_PENDING_RECLAIM_SECS,
                    "pruned stale pending idempotency record"
                );
            }

            match tree.compare_and_swap(key.as_ref(), Some(raw.as_ref()), None as Option<&[u8]>) {
                Ok(Ok(())) => {
                    removed += 1;
                }
                Ok(Err(_)) => continue,
                Err(e) => {
                    return Err(anyhow::anyhow!(
                        "failed to prune expired idempotency record atomically: {}",
                        e
                    ))
                }
            }
        }

        if removed > 0 {
            tree.flush()?;
        }
        Ok(removed)
    }

    // ---- Scan block metadata ----

    fn scan_block_meta_key(vault_pub_key: &str, height: u64) -> String {
        format!("{}:{:020}", vault_pub_key, height)
    }

    pub fn save_scan_block_meta(&self, vault_pub_key: &str, meta: &ScanBlockMeta) -> Result<()> {
        let tree = self.db.open_tree("scan_block_meta")?;
        let key = Self::scan_block_meta_key(vault_pub_key, meta.height);
        tree.insert(key.as_bytes(), serde_json::to_vec(meta)?)?;
        tree.flush()?;
        Ok(())
    }

    pub fn load_scan_block_meta(
        &self,
        vault_pub_key: &str,
        height: u64,
    ) -> Result<Option<ScanBlockMeta>> {
        let tree = self.db.open_tree("scan_block_meta")?;
        let key = Self::scan_block_meta_key(vault_pub_key, height);
        let Some(raw) = tree.get(key.as_bytes())? else {
            return Ok(None);
        };
        let meta: ScanBlockMeta = serde_json::from_slice(&raw)?;
        Ok(Some(meta))
    }

    pub fn delete_scan_block_meta(&self, vault_pub_key: &str, height: u64) -> Result<()> {
        let tree = self.db.open_tree("scan_block_meta")?;
        let key = Self::scan_block_meta_key(vault_pub_key, height);
        tree.remove(key.as_bytes())?;
        tree.flush()?;
        Ok(())
    }

    pub fn rollback_scan_state_from_height(
        &self,
        vault_pub_key: &str,
        from_height: u64,
    ) -> Result<()> {
        let tree = self.db.open_tree("scan_block_meta")?;
        let prefix = format!("{vault_pub_key}:");
        let mut to_delete = Vec::new();
        let mut refs_to_remove: HashSet<(String, u32)> = HashSet::new();

        for item in tree.scan_prefix(prefix.as_bytes()) {
            let (key, value) = item?;
            let meta: ScanBlockMeta = serde_json::from_slice(&value)?;
            if meta.height < from_height {
                continue;
            }
            for r in &meta.added_outputs {
                refs_to_remove.insert((r.tx_hash.clone(), r.output_index));
            }
            to_delete.push(key);
        }

        for key in to_delete {
            tree.remove(key)?;
        }
        tree.flush()?;

        // Use CAS to remove rolled-back outputs without clobbering
        // concurrent reservation state changes.
        if !refs_to_remove.is_empty() {
            self.cas_update_vault_tracked_state(vault_pub_key, |state| {
                state.outputs.retain(|output| {
                    !refs_to_remove.contains(&(output.tx_hash.clone(), output.output_index))
                });
                state.collision_records = rebuild_collision_records(&state.outputs);
                Ok(())
            })?;
        }
        self.save_scan_height(vault_pub_key, from_height.saturating_sub(1))?;
        Ok(())
    }

    pub fn prune_scan_block_meta_before(
        &self,
        vault_pub_key: &str,
        min_height: u64,
    ) -> Result<usize> {
        let tree = self.db.open_tree("scan_block_meta")?;
        let prefix = format!("{vault_pub_key}:");
        let mut to_delete = Vec::new();

        for item in tree.scan_prefix(prefix.as_bytes()) {
            let (key, value) = item?;
            let meta: ScanBlockMeta = serde_json::from_slice(&value)?;
            if meta.height < min_height {
                to_delete.push(key);
            }
        }

        for key in &to_delete {
            tree.remove(key)?;
        }
        tree.flush()?;
        Ok(to_delete.len())
    }


    /// Save the last scanned height for a vault.
    pub fn save_scan_height(&self, vault_pub_key: &str, height: u64) -> Result<()> {
        let tree = self.db.open_tree("scan_heights")?;
        tree.insert(vault_pub_key.as_bytes(), &height.to_be_bytes())?;
        tree.flush()?;
        Ok(())
    }

    /// Load the last scanned height for a vault.
    pub fn load_scan_height(&self, vault_pub_key: &str) -> Result<Option<u64>> {
        let tree = self.db.open_tree("scan_heights")?;
        match tree.get(vault_pub_key.as_bytes())? {
            Some(bytes) => {
                if bytes.len() != 8 {
                    warn!(vault = vault_pub_key, "corrupt scan height entry");
                    return Ok(None);
                }
                let height = u64::from_be_bytes(bytes.as_ref().try_into().unwrap());
                Ok(Some(height))
            }
            None => Ok(None),
        }
    }
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use std::sync::{Arc, Barrier};
    use std::thread;

    use dkg_pedpop::{KeyGenMachine, Participant, ThresholdParams};

    use super::*;

    fn generate_valid_threshold_key_payload() -> Vec<u8> {
        let participant = Participant::new(1).expect("participant index must be valid");
        let params = ThresholdParams::new(1, 1, participant).expect("valid 1-of-1 threshold");
        let machine = KeyGenMachine::<FrostEd25519>::new(params, [7u8; 32]);
        let (secret_share_machine, _) = machine.generate_coefficients(&mut rand::rngs::OsRng);
        let (key_machine, _) = secret_share_machine
            .generate_secret_shares(&mut rand::rngs::OsRng, HashMap::new())
            .expect("round2 should succeed for 1-of-1");
        let blame_machine = key_machine
            .calculate_share(&mut rand::rngs::OsRng, HashMap::new())
            .expect("round3 should succeed for 1-of-1");
        let threshold_keys = blame_machine.complete();
        threshold_keys.serialize().to_vec()
    }

    #[test]
    fn save_and_load_frost_keys_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let plaintext = b"frost-key-bytes";

        store.save_frost_keys("vault-a", plaintext).unwrap();
        let loaded = store.load_frost_keys("vault-a").unwrap().unwrap();
        assert_eq!(loaded, plaintext);

        let raw = std::fs::read(dir.path().join("keys").join("vault-a.frost")).unwrap();
        assert_eq!(raw, plaintext);
    }

    #[cfg(unix)]
    #[test]
    fn unix_key_material_permissions_are_hardened() {
        use std::os::unix::fs::PermissionsExt;

        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        store.save_frost_keys("vault-perm", b"secret").unwrap();

        let data_mode = std::fs::metadata(dir.path()).unwrap().permissions().mode() & 0o777;
        assert_eq!(data_mode, PRIVATE_DIR_MODE);

        let keys_dir = dir.path().join("keys");
        let keys_mode = std::fs::metadata(&keys_dir).unwrap().permissions().mode() & 0o777;
        assert_eq!(keys_mode, PRIVATE_DIR_MODE);

        let key_path = keys_dir.join("vault-perm.frost");
        let key_mode = std::fs::metadata(key_path).unwrap().permissions().mode() & 0o777;
        assert_eq!(key_mode, PRIVATE_FILE_MODE);
    }

    #[test]
    fn startup_sweep_rejects_invalid_payload() {
        let dir = tempfile::tempdir().unwrap();
        let _store = Store::open(dir.path()).unwrap();

        let key_dir = dir.path().join("keys");
        std::fs::create_dir_all(&key_dir).unwrap();
        std::fs::write(key_dir.join("vault-c.frost"), [0u8; 32]).unwrap();

        let reopened = Store::open(dir.path());
        assert!(reopened.is_err());
    }

    #[test]
    fn startup_sweep_passes_for_valid_v2_key_files() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let valid_payload = generate_valid_threshold_key_payload();
        store
            .save_frost_keys("vault-valid", &valid_payload)
            .unwrap();
        drop(store);

        let reopened = Store::open(dir.path());
        assert!(reopened.is_ok());
    }

    #[test]
    fn startup_sweep_fails_on_tampered_v2_key_file() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let valid_payload = generate_valid_threshold_key_payload();
        store
            .save_frost_keys("vault-tamper", &valid_payload)
            .unwrap();
        drop(store);

        let path = dir.path().join("keys").join("vault-tamper.frost");
        let mut raw = std::fs::read(&path).unwrap();
        let last = raw.len() - 1;
        raw[last] ^= 0x01;
        std::fs::write(&path, raw).unwrap();

        let reopened = Store::open(dir.path());
        assert!(reopened.is_err());
    }

    #[test]
    fn load_outputs_rejects_pre_cutover_records_without_state_and_wallet_output_payload() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();

        let pre_cutover_json = serde_json::json!([{
            "tx_hash": "txhash",
            "output_index": 0,
            "amount": 10,
            "height": 1,
            "key_image": null,
            "spent": false,
            "subaddress_major": 0,
            "subaddress_minor": 0
        }]);

        let tree = store.db.open_tree("outputs").unwrap();
        tree.insert(
            "vault-old".as_bytes(),
            serde_json::to_vec(&pre_cutover_json).unwrap(),
        )
        .unwrap();
        tree.flush().unwrap();

        let err = store
            .load_outputs("vault-old")
            .expect_err("pre-cutover output shape should fail strict decode")
            .to_string();
        assert!(
            err.contains("VaultTrackedState")
                || err.contains("missing field")
                || err.contains("invalid type")
        );
    }

    #[test]
    fn outgoing_transfer_roundtrip() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let record = OutgoingTransferRecord {
            tx_hash: "hash1".to_string(),
            tx_hex: "deadbeef".to_string(),
            msg_id: "msg1".to_string(),
            vault_pub_key: "thorpub1vault".to_string(),
            from_address: "48from".to_string(),
            to_address: "48to".to_string(),
            amount: 42,
            fee: 2,
            memo: "OUT:ABC".to_string(),
            planned_height: 12,
            observed_height: Some(12),
            status: "observed_mined".to_string(),
            broadcasted_at: Some(123),
            observed_at: Some(123),
            created_at: 123,
        };

        store.save_outgoing_transfer(&record).unwrap();
        let loaded = store.load_outgoing_transfers("thorpub1vault").unwrap();
        assert_eq!(loaded.len(), 1);
        assert_eq!(loaded[0].tx_hash, "hash1");
        assert_eq!(loaded[0].from_address, "48from");
    }

    fn sample_idempotency_record(route: &str, msg_id: &str, created_at: i64) -> IdempotencyRecord {
        IdempotencyRecord {
            route: route.to_string(),
            msg_id: msg_id.to_string(),
            payload_hash: "payload-hash".to_string(),
            response_hash: "response-hash".to_string(),
            status_code: 200,
            response_json: serde_json::json!({ "ok": true }),
            created_at,
            state: IdempotencyState::Complete,
        }
    }

    fn sample_pending_idempotency_record(
        route: &str,
        msg_id: &str,
        payload_hash: &str,
        created_at: i64,
    ) -> IdempotencyRecord {
        IdempotencyRecord {
            route: route.to_string(),
            msg_id: msg_id.to_string(),
            payload_hash: payload_hash.to_string(),
            response_hash: String::new(),
            status_code: 0,
            response_json: serde_json::Value::Null,
            created_at,
            state: IdempotencyState::Pending,
        }
    }

    fn sample_reserved_output(msg_id: &str, tx_hash_candidate: Option<&str>) -> TrackedOutput {
        TrackedOutput {
            tx_hash: "spent-input-tx".to_string(),
            output_index: 0,
            amount: 42,
            height: 100,
            one_time_key: Some("one-time-key".to_string()),
            key_image: Some("key-image".to_string()),
            state: OutputState::Reserved,
            reservation: Some(OutputReservation {
                session_id: "session-1".to_string(),
                msg_id: msg_id.to_string(),
                reserved_at: 1_000,
                expires_at: 2_000,
                tx_hash_candidate: tx_hash_candidate.map(ToString::to_string),
            }),
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("abcd".to_string()),
            address: Some("48from".to_string()),
            note: None,
        }
    }

    #[test]
    fn release_reservations_for_session_releases_only_matching_bindings() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();

        let mut matching = sample_reserved_output("msg-1", None);
        matching.tx_hash = "matching".to_string();
        matching.output_index = 0;

        let mut other_msg = sample_reserved_output("msg-2", None);
        other_msg.tx_hash = "other-msg".to_string();
        other_msg.output_index = 1;

        let mut other_session = sample_reserved_output("msg-1", None);
        other_session.tx_hash = "other-session".to_string();
        other_session.output_index = 2;
        if let Some(reservation) = other_session.reservation.as_mut() {
            reservation.session_id = "session-2".to_string();
        }

        store
            .save_outputs("vault", &[matching, other_msg, other_session])
            .unwrap();
        let released = store
            .release_reservations_for_session("vault", "session-1", "msg-1")
            .unwrap();
        assert_eq!(released, 1);

        let outputs = store.load_outputs("vault").unwrap();
        assert_eq!(outputs.len(), 3);
        assert_eq!(outputs[0].state, OutputState::Unspent);
        assert!(outputs[0].reservation.is_none());
        assert_eq!(outputs[1].state, OutputState::Reserved);
        assert!(outputs[1].reservation.is_some());
        assert_eq!(outputs[2].state, OutputState::Reserved);
        assert!(outputs[2].reservation.is_some());
    }

    #[test]
    fn release_reservations_for_session_requires_non_empty_identity_fields() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();

        let err = store
            .release_reservations_for_session(" ", "session-1", "msg-1")
            .unwrap_err()
            .to_string();
        assert!(err.contains("vault_pub_key is required"));

        let err = store
            .release_reservations_for_session("vault", " ", "msg-1")
            .unwrap_err()
            .to_string();
        assert!(err.contains("session_id is required"));

        let err = store
            .release_reservations_for_session("vault", "session-1", " ")
            .unwrap_err()
            .to_string();
        assert!(err.contains("msg_id is required"));
    }

    #[test]
    fn finalize_reserved_for_msg_requires_non_empty_tx_hash() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let err = store
            .finalize_reserved_for_msg("vault", "msg-1", "   ")
            .unwrap_err()
            .to_string();
        assert!(err.contains("tx_hash is required"));
    }

    #[test]
    fn finalize_reserved_for_msg_does_not_finalize_missing_candidate() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        store
            .save_outputs("vault", &[sample_reserved_output("msg-1", None)])
            .unwrap();

        let result = store
            .finalize_reserved_for_msg("vault", "msg-1", "abc123")
            .unwrap();
        assert_eq!(result.finalized, 0);
        assert_eq!(result.missing_candidate, 1);
        assert_eq!(result.candidate_mismatch, 0);

        let outputs = store.load_outputs("vault").unwrap();
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].state, OutputState::Reserved);
        assert!(outputs[0].reservation.is_some());
    }

    #[test]
    fn finalize_reserved_for_msg_does_not_finalize_candidate_mismatch() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        store
            .save_outputs(
                "vault",
                &[sample_reserved_output("msg-1", Some("expected-hash"))],
            )
            .unwrap();

        let result = store
            .finalize_reserved_for_msg("vault", "msg-1", "different-hash")
            .unwrap();
        assert_eq!(result.finalized, 0);
        assert_eq!(result.missing_candidate, 0);
        assert_eq!(result.candidate_mismatch, 1);

        let outputs = store.load_outputs("vault").unwrap();
        assert_eq!(outputs.len(), 1);
        assert_eq!(outputs[0].state, OutputState::Reserved);
        assert!(outputs[0].reservation.is_some());
    }

    #[test]
    fn idempotency_claim_lifecycle_transitions() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let now_ts = 1_000_000_i64;
        let route = "sign_prepare";
        let msg_id = "msg-1";
        let payload_hash = "payload-a";

        let fresh = store
            .claim_idempotency(route, msg_id, payload_hash, now_ts)
            .unwrap();
        assert!(matches!(fresh, IdempotencyClaimResult::Fresh));

        let in_progress = store
            .claim_idempotency(route, msg_id, payload_hash, now_ts)
            .unwrap();
        assert!(matches!(in_progress, IdempotencyClaimResult::InProgress));

        let conflict = store
            .claim_idempotency(route, msg_id, "payload-b", now_ts)
            .unwrap();
        assert!(matches!(conflict, IdempotencyClaimResult::Conflict));

        let response = serde_json::json!({ "ok": true });
        store
            .complete_idempotency(route, msg_id, payload_hash, "response-hash", 200, &response)
            .unwrap();

        let replay = store
            .claim_idempotency(route, msg_id, payload_hash, now_ts)
            .unwrap();
        match replay {
            IdempotencyClaimResult::Replay(record) => {
                assert_eq!(record.status_code, 200);
                assert_eq!(record.response_json, response);
                assert_eq!(record.state, IdempotencyState::Complete);
            }
            other => panic!("expected replay after completion, got {other:?}"),
        }

        let post_complete_conflict = store
            .claim_idempotency(route, msg_id, "payload-b", now_ts)
            .unwrap();
        assert!(matches!(
            post_complete_conflict,
            IdempotencyClaimResult::Conflict
        ));
    }

    #[test]
    fn concurrent_claims_different_payloads_are_not_both_fresh() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        let barrier = Arc::new(Barrier::new(2));
        let now_ts = 2_000_000_i64;

        let store_a = store.clone();
        let barrier_a = barrier.clone();
        let a = thread::spawn(move || {
            barrier_a.wait();
            store_a
                .claim_idempotency("sign_partial", "msg-race", "payload-a", now_ts)
                .unwrap()
        });

        let store_b = store.clone();
        let barrier_b = barrier.clone();
        let b = thread::spawn(move || {
            barrier_b.wait();
            store_b
                .claim_idempotency("sign_partial", "msg-race", "payload-b", now_ts)
                .unwrap()
        });

        let results = [a.join().unwrap(), b.join().unwrap()];
        let fresh_count = results
            .iter()
            .filter(|result| matches!(result, IdempotencyClaimResult::Fresh))
            .count();
        let conflict_count = results
            .iter()
            .filter(|result| matches!(result, IdempotencyClaimResult::Conflict))
            .count();

        assert_eq!(fresh_count, 1, "exactly one claim may win the fresh slot");
        assert_eq!(
            conflict_count, 1,
            "concurrent different payload claims must conflict for one caller"
        );
    }

    #[test]
    fn pending_claim_younger_than_reclaim_window_returns_in_progress() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let now_ts = 3_000_000_i64;
        let route = "sign_partial";
        let msg_id = "msg-pending-young";
        let payload_hash = "payload-young";

        let fresh = store
            .claim_idempotency(route, msg_id, payload_hash, now_ts)
            .unwrap();
        assert!(matches!(fresh, IdempotencyClaimResult::Fresh));

        let in_progress = store
            .claim_idempotency(
                route,
                msg_id,
                payload_hash,
                now_ts + IDEMPOTENCY_PENDING_RECLAIM_SECS - 1,
            )
            .unwrap();
        assert!(matches!(in_progress, IdempotencyClaimResult::InProgress));
    }

    #[test]
    fn stale_pending_claim_is_reclaimed_and_reclaimed_claim_is_fresh() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let now_ts = 4_000_000_i64;
        let route = "sign_prepare";
        let msg_id = "msg-pending-stale";
        let payload_hash = "payload-stale";
        let stale_pending = sample_pending_idempotency_record(
            route,
            msg_id,
            payload_hash,
            now_ts - IDEMPOTENCY_PENDING_RECLAIM_SECS - 1,
        );
        store.save_idempotency(&stale_pending).unwrap();

        let claim = store
            .claim_idempotency(route, msg_id, payload_hash, now_ts)
            .unwrap();
        assert!(matches!(claim, IdempotencyClaimResult::Fresh));

        let loaded = store
            .get_idempotency(route, msg_id, now_ts)
            .unwrap()
            .expect("reclaimed stale pending should be replaced by fresh pending");
        assert_eq!(loaded.created_at, now_ts);
        assert_eq!(loaded.state, IdempotencyState::Pending);
    }

    #[test]
    fn concurrent_stale_pending_reclaim_allows_single_fresh_winner() {
        let dir = tempfile::tempdir().unwrap();
        let store = Arc::new(Store::open(dir.path()).unwrap());
        let route = "sign_combine";
        let msg_id = "msg-stale-race";
        let payload_hash = "payload-race";
        let now_ts = 5_000_000_i64;
        let stale_pending = sample_pending_idempotency_record(
            route,
            msg_id,
            payload_hash,
            now_ts - IDEMPOTENCY_PENDING_RECLAIM_SECS - 1,
        );
        store.save_idempotency(&stale_pending).unwrap();

        let barrier = Arc::new(Barrier::new(2));
        let store_a = store.clone();
        let barrier_a = barrier.clone();
        let a = thread::spawn(move || {
            barrier_a.wait();
            store_a
                .claim_idempotency(route, msg_id, payload_hash, now_ts)
                .unwrap()
        });
        let store_b = store.clone();
        let barrier_b = barrier.clone();
        let b = thread::spawn(move || {
            barrier_b.wait();
            store_b
                .claim_idempotency(route, msg_id, payload_hash, now_ts)
                .unwrap()
        });

        let results = [a.join().unwrap(), b.join().unwrap()];
        let fresh_count = results
            .iter()
            .filter(|result| matches!(result, IdempotencyClaimResult::Fresh))
            .count();
        let in_progress_count = results
            .iter()
            .filter(|result| matches!(result, IdempotencyClaimResult::InProgress))
            .count();

        assert_eq!(fresh_count, 1, "exactly one claim may win the fresh slot");
        assert_eq!(
            in_progress_count, 1,
            "second concurrent claimant should observe in-progress after stale reclaim"
        );
    }

    #[test]
    fn get_idempotency_evicts_expired_record() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let now_ts = 1_000_000i64;
        let record = sample_idempotency_record(
            "sign_prepare",
            "msg-expired",
            now_ts - IDEMPOTENCY_RETENTION_SECS - 1,
        );

        store.save_idempotency(&record).unwrap();

        let loaded = store
            .get_idempotency("sign_prepare", "msg-expired", now_ts)
            .unwrap();
        assert!(loaded.is_none());

        let tree = store.db.open_tree("idempotency").unwrap();
        assert!(tree
            .get("sign_prepare:msg-expired".as_bytes())
            .unwrap()
            .is_none());
    }

    #[test]
    fn get_idempotency_returns_unexpired_record() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let now_ts = 1_000_000i64;
        let record = sample_idempotency_record(
            "sign_prepare",
            "msg-fresh",
            now_ts - IDEMPOTENCY_RETENTION_SECS + 1,
        );

        store.save_idempotency(&record).unwrap();

        let loaded = store
            .get_idempotency("sign_prepare", "msg-fresh", now_ts)
            .unwrap()
            .expect("idempotency record should be retained");
        assert_eq!(loaded.msg_id, "msg-fresh");
        assert_eq!(loaded.route, "sign_prepare");
    }

    #[test]
    fn prune_expired_idempotency_removes_only_stale_entries() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let now_ts = 1_000_000i64;
        let expired_a = sample_idempotency_record(
            "sign_prepare",
            "msg-expired-a",
            now_ts - IDEMPOTENCY_RETENTION_SECS - 1,
        );
        let expired_b = sample_idempotency_record(
            "sign_partial",
            "msg-expired-b",
            now_ts - IDEMPOTENCY_RETENTION_SECS - 10,
        );
        let fresh = sample_idempotency_record(
            "sign_combine",
            "msg-fresh",
            now_ts - IDEMPOTENCY_RETENTION_SECS + 5,
        );

        store.save_idempotency(&expired_a).unwrap();
        store.save_idempotency(&expired_b).unwrap();
        store.save_idempotency(&fresh).unwrap();

        let removed = store.prune_expired_idempotency(now_ts).unwrap();
        assert_eq!(removed, 2);

        let tree = store.db.open_tree("idempotency").unwrap();
        assert!(tree
            .get("sign_prepare:msg-expired-a".as_bytes())
            .unwrap()
            .is_none());
        assert!(tree
            .get("sign_partial:msg-expired-b".as_bytes())
            .unwrap()
            .is_none());
        assert!(tree
            .get("sign_combine:msg-fresh".as_bytes())
            .unwrap()
            .is_some());
    }

    #[test]
    fn prune_expired_idempotency_removes_stale_pending_and_keeps_fresh_complete() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let now_ts = 6_000_000_i64;
        let stale_pending = sample_pending_idempotency_record(
            "sign_prepare",
            "msg-pending-stale-prune",
            "payload-prune",
            now_ts - IDEMPOTENCY_PENDING_RECLAIM_SECS - 1,
        );
        let fresh_complete =
            sample_idempotency_record("sign_prepare", "msg-complete-fresh", now_ts - 5);

        store.save_idempotency(&stale_pending).unwrap();
        store.save_idempotency(&fresh_complete).unwrap();

        let removed = store.prune_expired_idempotency(now_ts).unwrap();
        assert_eq!(removed, 1);

        let tree = store.db.open_tree("idempotency").unwrap();
        assert!(tree
            .get("sign_prepare:msg-pending-stale-prune".as_bytes())
            .unwrap()
            .is_none());
        assert!(tree
            .get("sign_prepare:msg-complete-fresh".as_bytes())
            .unwrap()
            .is_some());
    }

    #[test]
    fn rollback_scan_state_from_height_removes_outputs_and_metadata() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let vault = "thorpub1vault";

        let output_a = TrackedOutput {
            tx_hash: "txa".to_string(),
            output_index: 0,
            amount: 10,
            height: 10,
            one_time_key: Some("one-time-key-a".to_string()),
            key_image: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("aa".to_string()),
            address: Some("48a".to_string()),
            note: Some("a".to_string()),
        };
        let output_b = TrackedOutput {
            tx_hash: "txb".to_string(),
            output_index: 1,
            amount: 20,
            height: 11,
            one_time_key: Some("one-time-key-b".to_string()),
            key_image: None,
            state: OutputState::Unspent,
            reservation: None,
            subaddress_major: 0,
            subaddress_minor: 0,
            wallet_output_hex: Some("bb".to_string()),
            address: Some("48b".to_string()),
            note: Some("b".to_string()),
        };

        store
            .save_outputs(vault, &[output_a.clone(), output_b.clone()])
            .unwrap();
        store
            .save_scan_block_meta(
                vault,
                &ScanBlockMeta {
                    height: 10,
                    block_hash: "h10".to_string(),
                    parent_hash: "h9".to_string(),
                    added_outputs: vec![OutputRef {
                        tx_hash: output_a.tx_hash.clone(),
                        output_index: output_a.output_index,
                    }],
                    credited_outputs: vec![CreditedOutput {
                        tx_hash: output_a.tx_hash.clone(),
                        output_index: output_a.output_index,
                        amount: output_a.amount,
                    }],
                },
            )
            .unwrap();
        store
            .save_scan_block_meta(
                vault,
                &ScanBlockMeta {
                    height: 11,
                    block_hash: "h11".to_string(),
                    parent_hash: "h10".to_string(),
                    added_outputs: vec![OutputRef {
                        tx_hash: output_b.tx_hash.clone(),
                        output_index: output_b.output_index,
                    }],
                    credited_outputs: vec![CreditedOutput {
                        tx_hash: output_b.tx_hash.clone(),
                        output_index: output_b.output_index,
                        amount: output_b.amount,
                    }],
                },
            )
            .unwrap();
        store.save_scan_height(vault, 11).unwrap();

        store.rollback_scan_state_from_height(vault, 11).unwrap();
        let outputs_after = store.load_outputs(vault).unwrap();
        assert_eq!(outputs_after.len(), 1);
        assert_eq!(outputs_after[0].tx_hash, "txa");
        assert!(store.load_scan_block_meta(vault, 10).unwrap().is_some());
        assert!(store.load_scan_block_meta(vault, 11).unwrap().is_none());
        assert_eq!(store.load_scan_height(vault).unwrap(), Some(10));
    }

    #[test]
    fn prune_scan_block_meta_before_removes_only_older_entries() {
        let dir = tempfile::tempdir().unwrap();
        let store = Store::open(dir.path()).unwrap();
        let vault = "thorpub1vault";

        for height in [9u64, 10, 11] {
            store
                .save_scan_block_meta(
                    vault,
                    &ScanBlockMeta {
                        height,
                        block_hash: format!("h{height}"),
                        parent_hash: format!("h{}", height.saturating_sub(1)),
                        added_outputs: Vec::new(),
                        credited_outputs: Vec::new(),
                    },
                )
                .unwrap();
        }

        let removed = store.prune_scan_block_meta_before(vault, 11).unwrap();
        assert_eq!(removed, 2);
        assert!(store.load_scan_block_meta(vault, 9).unwrap().is_none());
        assert!(store.load_scan_block_meta(vault, 10).unwrap().is_none());
        assert!(store.load_scan_block_meta(vault, 11).unwrap().is_some());
    }
}
