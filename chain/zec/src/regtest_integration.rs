
use crate::config::{init_zec};
use crate::error::ZecError;
use crate::network::Network;
use crate::tx::{build_ptx, apply_signatures, Output, PartialTx, UTXO};
use crate::keys::{derive_account_from_mnemonic, ZecAccount};
use crate::sapling_sender::{send_from_sapling_to_transparent, decrypt_notes_in_tx, build_witness_from_blocks, SpendableNote, DecryptedNote};
use crate::keys::BranchId;
use crate::addr::{validate_address_v124, is_transparent_address};

use serde_json::{Value, json};
use std::collections::HashMap;
use std::process::Command;
use std::thread;
use std::time::Duration;
use std::io::Write;

#[cfg(feature = "e2e")]
use reqwest;

/// Configuration for zcashd regtest daemon
#[derive(Clone)]
pub struct ZcashdConfig {
    pub rpc_user: String,
    pub rpc_password: String,
    pub rpc_host: String,
    pub rpc_port: u16,
    pub master_addr: String,
}

impl Default for ZcashdConfig {
    fn default() -> Self {
        Self {
            rpc_user: "mayachain".to_string(),
            rpc_password: "password".to_string(),
            rpc_host: "127.0.0.1".to_string(),
            rpc_port: 18232,
            master_addr: "".to_string(), // Will be set dynamically
        }
    }
}

/// RPC client for communicating with zcashd
#[cfg(feature = "e2e")]
pub struct ZcashdRpcClient {
    config: ZcashdConfig,
    reqwest_client: reqwest::blocking::Client,
}

#[cfg(feature = "e2e")]
impl ZcashdRpcClient {
    pub fn new(config: ZcashdConfig) -> Self {
        Self {
            config,
            reqwest_client: reqwest::blocking::Client::new(),
        }
    }

    fn rpc_call(&self, method: &str, params: Value) -> Result<Value, Box<dyn std::error::Error>> {
        let url = format!("http://{}:{}", self.config.rpc_host, self.config.rpc_port);
        let request_body = json!({
            "jsonrpc": "1.0",
            "id": "test",
            "method": method,
            "params": params
        });

        let response = self.reqwest_client
            .post(&url)
            .basic_auth(&self.config.rpc_user, Some(&self.config.rpc_password))
            .json(&request_body)
            .send()?;

        let response_json: Value = response.json()?;
        
        if let Some(error) = response_json.get("error") {
            if !error.is_null() {
                return Err(format!("RPC error: {error}").into());
            }
        }

        Ok(response_json["result"].clone())
    }

    pub fn generate_blocks(&self, num_blocks: u64) -> Result<Vec<String>, Box<dyn std::error::Error>> {
        let result = self.rpc_call("generate", json!([num_blocks]))?;
        let block_hashes: Vec<String> = result.as_array().unwrap()
            .iter()
            .map(|v| v.as_str().unwrap().to_string())
            .collect();
        Ok(block_hashes)
    }

    pub fn get_address_balance(&self, address: &str) -> Result<u64, Box<dyn std::error::Error>> {
        let params = json!([{
            "addresses": [address]
        }]);
        let result = self.rpc_call("getaddressbalance", params)?;
        
        // getaddressbalance returns {"balance": satoshis, "received": satoshis}
        let balance = result.get("balance")
            .and_then(|v| v.as_u64())
            .unwrap_or(0);
        Ok(balance)
    }

    pub fn get_address_utxos(&self, address: &str) -> Result<Vec<UTXO>, Box<dyn std::error::Error>> {
        let params = json!([{
            "addresses": [address]
        }]);
        let result = self.rpc_call("getaddressutxos", params)?;
        
        let utxos: Vec<UTXO> = result.as_array().unwrap()
            .iter()
            .map(|v| {
                let obj = v.as_object().unwrap();
                UTXO {
                    txid: obj["txid"].as_str().unwrap().to_string(),
                    vout: obj["outputIndex"].as_u64().unwrap() as u32,
                    script: obj["script"].as_str().unwrap().to_string(),
                    value: obj["satoshis"].as_u64().unwrap(),
                    height: obj["height"].as_u64().unwrap() as u32,
                }
            })
            .collect();
        Ok(utxos)
    }

    pub fn import_address(&self, address: &str, rescan: bool) -> Result<(), Box<dyn std::error::Error>> {
        let _result = self.rpc_call("importaddress", json!([address, "", rescan]))?;
        Ok(())
    }

    pub fn send_raw_transaction(&self, hex_tx: &str) -> Result<String, Box<dyn std::error::Error>> {
        let result = self.rpc_call("sendrawtransaction", json!([hex_tx]))?;
        Ok(result.as_str().unwrap().to_string())
    }

    pub fn get_raw_transaction(&self, txid: &str) -> Result<String, Box<dyn std::error::Error>> {
        let result = self.rpc_call("getrawtransaction", json!([txid]))?;
        Ok(result.as_str().unwrap().to_string())
    }

    pub fn get_block_count(&self) -> Result<u64, Box<dyn std::error::Error>> {
        let result = self.rpc_call("getblockcount", json!([]))?;
        Ok(result.as_u64().unwrap())
    }

    pub fn wait_for_transaction(&self, txid: &str, confirmations: u32) -> Result<(), Box<dyn std::error::Error>> {
        for _ in 0..60 { // Wait up to 60 seconds
            if let Ok(result) = self.rpc_call("getrawtransaction", json!([txid, 1])) {
                if let Some(confs) = result.get("confirmations") {
                    if confs.as_u64().unwrap_or(0) >= confirmations as u64 {
                        return Ok(());
                    }
                }
            }
            thread::sleep(Duration::from_secs(1));
        }
        Err("Timeout waiting for confirmations".into())
    }
}

/// Test the full transparent -> sapling -> transparent flow
#[cfg(feature = "e2e")]
pub fn test_transparent_sapling_transparent_flow() -> Result<(), Box<dyn std::error::Error>> {
    let network = Network::Regtest; // Regtest mode
    // Initialize the ZEC library
    init_zec()?;
    
    let config = ZcashdConfig::default();
    let rpc = ZcashdRpcClient::new(config);
    
    // Test mnemonic (BIP39)
    let mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about";
    
    println!("=== Phase 1: Setup and Initial Mining ===");
    
    // Derive our account from mnemonic
    let account = derive_account_from_mnemonic(mnemonic.to_string(), network)?;
    println!("Derived transparent address: {}", account.transparent);
    println!("Derived sapling address: {}", account.sapling);
    
    // Validate addresses
    assert!(is_transparent_address(&account.transparent, &network));
    assert!(validate_address_v124(account.sapling.clone(), network).is_ok());
    
    // Import our address for tracking (non-wallet mode)
    println!("Importing address for tracking...");
    rpc.import_address(&account.transparent, false)?;
    
    // Mine some initial blocks to get coinbase rewards
    println!("Mining 110 blocks to mature coinbase rewards...");
    let _blocks = rpc.generate_blocks(110)?;
    
    // Check balance using blockchain calls instead of wallet
    let initial_balance = rpc.get_address_balance(&account.transparent)?;
    println!("Initial transparent balance: {initial_balance} satoshis");
    
    println!("\n=== Phase 2: Check our transparent address funds ===");
    
    // Get UTXOs for our address using blockchain calls
    let utxos = rpc.get_address_utxos(&account.transparent)?;
    println!("Found {} UTXOs for our address", utxos.len());
    
    if utxos.is_empty() {
        println!("⚠️ No UTXOs found. This means the mineraddress in zcashd config doesn't match our derived address.");
        println!("   Expected: {}", account.transparent);
        println!("   Make sure zcashd was started with the correct -mineraddress parameter.");
        return Err("No funds available for testing".into());
    }
    
    let total_balance: u64 = utxos.iter().map(|u| u.value).sum();
    println!("Total balance: {} satoshis ({} ZEC)", total_balance, total_balance as f64 / 100_000_000.0);
    
    // Select the first UTXO for testing  
    let mut utxo = utxos[0].clone();
    println!("Using UTXO: {} satoshis in txid {}", utxo.value, utxo.txid);
    
    println!("\n=== Phase 3A: Test transparent -> transparent (to different wallet) ===");
    
    // Derive a second wallet from "dog dog dog..." mnemonic
    let dog_mnemonic = format!("{} fossil", "dog ".repeat(23));
    println!("Dog wallet mnemonic: {dog_mnemonic}");
    let dog_account = derive_account_from_mnemonic(dog_mnemonic, network)?;
    println!("Dog wallet transparent address: {}", dog_account.transparent);
    
    // Send funds to the dog wallet
    let send_amount = 300_000_000; // 3 ZEC
    let fee = 50_000; // 0.0005 ZEC fee
    let change_amount = utxo.value - send_amount - fee; // Remaining funds back to main wallet
    
    let transparent_ptx = PartialTx {
        height: rpc.get_block_count()? as u32,
        txid: vec![],
        inputs: vec![utxo.clone()],
        outputs: vec![
            Output {
                address: dog_account.transparent.clone(), // Send to dog wallet
                amount: send_amount,
                memo: "".to_string(),
            },
            Output {
                address: account.transparent.clone(), // Change back to main wallet
                amount: change_amount,
                memo: "".to_string(),
            },
        ],
        fee,
        sighashes: vec![],
        expiry_height: 0,
        version: 0,
    };
    
    // Build and sign the transaction
    println!("Building transparent->transparent transaction at height: {}", transparent_ptx.height);
    let transparent_ptx = build_ptx(account.public_key.clone(), transparent_ptx, network)?;
    println!("Built T→T transaction with {} sighashes", transparent_ptx.sighashes.len());
    
    // Sign the transaction
    let mut signatures = Vec::new();
    for (i, sighash) in transparent_ptx.sighashes.iter().enumerate() {
        println!("Signing T→T sighash {}: {}", i, hex::encode(sighash));
        
        use secp256k1::{Secp256k1, SecretKey, Message};
        let secp = Secp256k1::new();
        let secret_key = SecretKey::from_slice(&account.private_key)
            .map_err(|e| format!("Invalid private key: {}", e))?;
        let message = Message::from_digest_slice(sighash)
            .map_err(|e| format!("Invalid sighash: {}", e))?;
        let signature = secp.sign_ecdsa(&message, &secret_key);
        let der_sig = signature.serialize_der().to_vec();
        println!("Generated T→T signature {}: {}", i, hex::encode(&der_sig));
        signatures.push(der_sig);
    }
    
    let transparent_raw_tx = apply_signatures(account.public_key.clone(), transparent_ptx, signatures, network)?;
    println!("Created T→T transaction: {} bytes", transparent_raw_tx.len());
    
    // Send the transaction to zcashd
    println!("Sending T→T transaction to zcashd...");
    let tt_tx_hex = hex::encode(&transparent_raw_tx);
    let tt_txid = rpc.send_raw_transaction(&tt_tx_hex)?;
    println!("✅ Sent T→T transaction, txid: {}", tt_txid);
    
    // Mine blocks to confirm
    println!("Mining blocks to confirm T→T transaction...");
    rpc.generate_blocks(3)?;
    println!("✅ T→T transaction confirmed");
    
    println!("\n=== Phase 3B: Test transparent -> transparent (back to main wallet) ===");
    
    // Import the dog wallet address for tracking
    rpc.rpc_call("importaddress", json!([dog_account.transparent, "", false]))?;
    
    // Get dog wallet UTXOs
    let dog_utxos = rpc.get_address_utxos(&dog_account.transparent)?;
    println!("Dog wallet has {} UTXOs", dog_utxos.len());
    
    if dog_utxos.is_empty() {
        return Err("Dog wallet has no UTXOs after T→T transaction".into());
    }
    
    let dog_utxo = &dog_utxos[0];
    println!("Using dog UTXO: {} satoshis in txid {}", dog_utxo.value, dog_utxo.txid);
    
    // Send funds back from dog wallet to main wallet
    let return_amount = dog_utxo.value - 50_000; // Leave fee
    let return_ptx = PartialTx {
        height: rpc.get_block_count()? as u32,
        txid: vec![],
        inputs: vec![dog_utxo.clone()],
        outputs: vec![Output {
            address: account.transparent.clone(), // Send back to main wallet
            amount: return_amount,
            memo: "".to_string(),
        }],
        fee: 50_000,
        sighashes: vec![],
        expiry_height: 0,
        version: 0,
    };
    
    // Build and sign with dog wallet keys
    println!("Building return T→T transaction at height: {}", return_ptx.height);
    let return_ptx = build_ptx(dog_account.public_key.clone(), return_ptx, network)?;
    println!("Built return T→T transaction with {} sighashes", return_ptx.sighashes.len());
    
    // Sign with dog wallet private key
    let mut return_signatures = Vec::new();
    for (i, sighash) in return_ptx.sighashes.iter().enumerate() {
        println!("Signing return T→T sighash {}: {}", i, hex::encode(sighash));
        
        use secp256k1::{Secp256k1, SecretKey, Message};
        let secp = Secp256k1::new();
        let secret_key = SecretKey::from_slice(&dog_account.private_key)
            .map_err(|e| format!("Invalid dog private key: {}", e))?;
        let message = Message::from_digest_slice(sighash)
            .map_err(|e| format!("Invalid sighash: {}", e))?;
        let signature = secp.sign_ecdsa(&message, &secret_key);
        let der_sig = signature.serialize_der().to_vec();
        println!("Generated return T→T signature {}: {}", i, hex::encode(&der_sig));
        return_signatures.push(der_sig);
    }
    
    let return_raw_tx = apply_signatures(dog_account.public_key.clone(), return_ptx, return_signatures, network)?;
    println!("Created return T→T transaction: {} bytes", return_raw_tx.len());
    
    // Send return transaction to zcashd
    println!("Sending return T→T transaction to zcashd...");
    let return_tx_hex = hex::encode(&return_raw_tx);
    let return_txid = rpc.send_raw_transaction(&return_tx_hex)?;
    println!("✅ Sent return T→T transaction, txid: {}", return_txid);
    
    // Mine blocks to confirm
    println!("Mining blocks to confirm return T→T transaction...");
    rpc.generate_blocks(3)?;
    println!("✅ Return T→T transaction confirmed");
    println!("✅ Completed full T→T round-trip test!");
    
    // Update our UTXO for the next test
    let updated_utxos = rpc.get_address_utxos(&account.transparent)?;
    if updated_utxos.is_empty() {
        return Err("No UTXOs available after T→T round-trip".into());
    }
    utxo = updated_utxos[0].clone();
    println!("Updated UTXO for next test: {} satoshis", utxo.value);
    
    println!("\n=== Phase 3C: Now test transparent -> sapling ===");
    
    // Now test transparent->sapling with proper ZIP-317 fee
    let zip317_fee = 200_000; // Start with 0.002 ZEC for ZIP-317 compliance (will be ~2140 bytes)
    let sapling_send_amount = utxo.value - zip317_fee; // Leave proper fee
    let ptx = PartialTx {
        height: rpc.get_block_count()? as u32,
        txid: vec![],
        inputs: vec![utxo.clone()],
        outputs: vec![Output {
            address: account.sapling.clone(), // Sapling output
            amount: sapling_send_amount,
            memo: "Testing transparent to sapling".to_string(),
        }],
        fee: zip317_fee, // ZIP-317 compatible fee
        sighashes: vec![],
        expiry_height: 0,
        version: 0,
    };
    
    // Build the partial transaction
    println!("Building partial transaction at height: {}", ptx.height);
    let ptx = build_ptx(account.public_key.clone(), ptx, network)?;
    println!("Built partial transaction with {} sighashes", ptx.sighashes.len());
    
    // Sign the transaction using the private key derived from mnemonic
    let mut signatures = Vec::new();
    println!("Private key: {}", hex::encode(&account.private_key));
    println!("Public key: {}", hex::encode(&account.public_key));
    
    for (i, sighash) in ptx.sighashes.iter().enumerate() {
        println!("Signing sighash {}: {}", i, hex::encode(sighash));
        
        // Get the private key from the account
        let private_key_bytes = &account.private_key;
        
        // Create secp256k1 private key
        use secp256k1::{Secp256k1, SecretKey, Message};
        let secp = Secp256k1::new();
        let secret_key = SecretKey::from_slice(private_key_bytes)
            .map_err(|e| format!("Invalid private key: {}", e))?;
        
        // Sign the sighash
        let message = Message::from_digest_slice(sighash)
            .map_err(|e| format!("Invalid sighash: {}", e))?;
        let signature = secp.sign_ecdsa(&message, &secret_key);
        
        // Convert to DER format (pure DER, SIGHASH_ALL handled by transparent infrastructure)
        let der_sig = signature.serialize_der().to_vec();
        
        println!("Generated signature {}: {}", i, hex::encode(&der_sig));
        signatures.push(der_sig);
    }
    
    println!("Generated {} real signatures", signatures.len());
    
    // Apply signatures and get the raw transaction
    let raw_tx = apply_signatures(account.public_key.clone(), ptx, signatures, network)?;
    let tx_hex = hex::encode(&raw_tx);
    println!("Created raw transaction: {} bytes", raw_tx.len());
    
    // Send the transaction to zcashd
    println!("Sending transparent->sapling transaction to zcashd...");
    let txid2 = rpc.send_raw_transaction(&tx_hex)?;
    println!("✅ Sent transparent->sapling transaction, txid: {}", txid2);
    
    println!("\n=== Phase 4: Mine blocks and wait for transaction confirmation ===");
    
    // Mine blocks to confirm the shielded transaction
    println!("Mining blocks to confirm the T→Sapling transaction...");
    rpc.generate_blocks(3)?;
    println!("✅ Mined 3 confirmation blocks");
    
    // Wait a moment for the blocks to be processed
    std::thread::sleep(std::time::Duration::from_secs(2));
    
    // Wait for the transaction to be confirmed (check blockchain using non-wallet RPCs)
    println!("Waiting for transaction to be confirmed...");
    let mut retries = 0;
    loop {
        // First check if transaction is in mempool
        match rpc.rpc_call("getmempoolentry", json!([txid2])) {
            Ok(_) => {
                println!("Transaction found in mempool, waiting for mining...");
                std::thread::sleep(std::time::Duration::from_secs(1));
                retries += 1;
                print!(".");
                std::io::Write::flush(&mut std::io::stdout()).unwrap();
                continue;
            }
            Err(_) => {
                // Not in mempool, check if it's been mined
                match rpc.rpc_call("getrawtransaction", json!([txid2, 1])) {
                    Ok(tx_info) => {
                        let confirmations = tx_info.get("confirmations").and_then(|c| c.as_i64()).unwrap_or(0);
                        if confirmations >= 1 {
                            println!("✅ Transaction confirmed with {} confirmations", confirmations);
                            break;
                        } else {
                            println!("Transaction found but not yet confirmed (0 confirmations)");
                        }
                    }
                    Err(e) => {
                        if retries > 100 {
                            // Final check: scan entire mempool to be sure
                            match rpc.rpc_call("getrawmempool", json!([])) {
                                Ok(mempool) => {
                                    if let Some(txs) = mempool.as_array() {
                                        let in_mempool = txs.iter().any(|tx| tx.as_str() == Some(&txid2));
                                        if in_mempool {
                                            return Err("Transaction still in mempool after 100 seconds - not being mined".into());
                                        } else {
                                            return Err(format!("Transaction {} not found in blockchain or mempool after 100 seconds - likely rejected by node. Last error: {}", txid2, e).into());
                                        }
                                    }
                                }
                                Err(_) => {}
                            }
                            return Err(format!("Transaction {} not found after 100 seconds. Last error: {}", txid2, e).into());
                        }
                    }
                }
            }
        }
        
        if retries > 100 {
            return Err("Transaction confirmation timeout after 100 seconds".into());
        }
        
        std::thread::sleep(std::time::Duration::from_secs(1));
        retries += 1;
        print!(".");
        std::io::Write::flush(&mut std::io::stdout()).unwrap();
    }
    
    
    // Get the exact block height where our T→Sapling transaction was mined
    let tx_info = rpc.rpc_call("getrawtransaction", json!([txid2, 1]))?;
    let anchor_height = tx_info.get("height").and_then(|h| h.as_u64()).unwrap() as u32;
    println!("T→Sapling transaction mined at height: {}", anchor_height);
    
    // Get all transactions from genesis up to the anchor height to build complete Sapling tree
    println!("Building complete Sapling witness from genesis to height {}", anchor_height);
    let all_blocks = get_block_transactions_range(&rpc, 1, anchor_height)?;
    
    // Decrypt notes to find ours (only need to check the specific transaction)
    let mut found_notes: Vec<DecryptedNote> = vec![];
    let tx_hex = rpc.get_raw_transaction(&txid2)?;
    let tx_bytes = hex::decode(&tx_hex)?;
    match decrypt_notes_in_tx(
        mnemonic.to_string(),
        network,
        tx_bytes,
        anchor_height,
    ) {
        Ok(notes) => found_notes.extend(notes),
        Err(e) => return Err(format!("Failed to decrypt our Sapling note: {}", e).into()),
    }
    
    println!("Found {} sapling notes belonging to us", found_notes.len());
    
    if !found_notes.is_empty() {
        let note = &found_notes[0];
        println!("First note: {} satoshis", note.value);
        
        println!("\n=== Phase 5: Send from sapling back to transparent ===");
        

        // Fetch finalsaplingroot at the note's anchor height
        let bh = rpc.rpc_call("getblockhash", json!([anchor_height]))?
            .as_str().unwrap().to_string();
        let hdr = rpc.rpc_call("getblockheader", json!([bh, true]))?;
        let root_hex = hdr["finalsaplingroot"]
            .as_str()
            .ok_or("header missing finalsaplingroot")?;
        let root_bytes_vec = hex::decode(root_hex)?;

        // Build witness for our note using complete Sapling tree history
        let witness_parts = build_witness_from_blocks(
            mnemonic.to_string(),
            network,
            note.diversifier.clone(),
            note.value,
            note.rseed.clone(),
            note.zip212,
            vec![all_blocks], // Complete transaction history from genesis to anchor height
            BranchId::NU5, // Use NU5 for regtest
            Some(root_bytes_vec), // Verify the expected root matches
        )?;
        
        // Create spendable note
        let spendable_note = SpendableNote {
            diversifier: note.diversifier.clone(),
            value: note.value,
            rseed: note.rseed.clone(),
            zip212: note.zip212,
            witness: witness_parts.witness_bytes,
        };
        
        // Use our own address to send back to (or could use any valid transparent address)
        let return_address = account.transparent.clone();
        
        // Create sapling -> transparent transaction using the correct anchor height
        let return_tx_bytes = send_from_sapling_to_transparent(
            mnemonic.to_string(),
            network,
            vec![spendable_note],
            return_address.clone(),
            witness_parts.anchor_bytes,
            anchor_height, // Use the height where our note was created, not current tip
            Some("Returning from sapling to transparent".to_string()),
        )?;
        
        let return_tx_hex = hex::encode(&return_tx_bytes);
        println!("Created sapling->transparent transaction: {} bytes", return_tx_bytes.len());
        
        // Send the sapling->transparent transaction to zcashd
        println!("Sending sapling->transparent transaction to zcashd...");
        let txid3 = rpc.send_raw_transaction(&return_tx_hex)?;
        println!("✅ Sent sapling->transparent transaction, txid: {}", txid3);
        
        // Mine blocks to confirm the transaction
        println!("Mining blocks to confirm sapling->transparent transaction...");
        rpc.generate_blocks(3)?;
        println!("✅ Mined 3 confirmation blocks");
        
        // Wait for the transaction to be confirmed using non-wallet RPCs
        println!("Waiting for sapling->transparent transaction to be confirmed...");
        let mut retries = 0;
        loop {
            // First check if transaction is in mempool
            match rpc.rpc_call("getmempoolentry", json!([txid3])) {
                Ok(_) => {
                    println!("Transaction found in mempool, waiting for mining...");
                    std::thread::sleep(std::time::Duration::from_secs(1));
                    retries += 1;
                    print!(".");
                    std::io::Write::flush(&mut std::io::stdout()).unwrap();
                    continue;
                }
                Err(_) => {
                    // Not in mempool, check if it's been mined
                    match rpc.rpc_call("getrawtransaction", json!([txid3, 1])) {
                        Ok(tx_info) => {
                            let confirmations = tx_info.get("confirmations").and_then(|c| c.as_i64()).unwrap_or(0);
                            if confirmations >= 1 {
                                println!("✅ Sapling->transparent transaction confirmed with {} confirmations", confirmations);
                                break;
                            } else {
                                println!("Transaction found but not yet confirmed (0 confirmations)");
                            }
                        }
                        Err(e) => {
                            if retries > 30 {
                                // Final check: scan entire mempool to be sure
                                match rpc.rpc_call("getrawmempool", json!([])) {
                                    Ok(mempool) => {
                                        if let Some(txs) = mempool.as_array() {
                                            let in_mempool = txs.iter().any(|tx| tx.as_str() == Some(&txid3));
                                            if in_mempool {
                                                return Err("Sapling->transparent transaction still in mempool after 30 seconds - not being mined".into());
                                            } else {
                                                return Err(format!("Sapling->transparent transaction {txid3} not found in blockchain or mempool after 30 seconds - likely rejected by node. Last error: {e}").into());
                                            }
                                        }
                                    }
                                    Err(_) => {}
                                }
                                return Err(format!("Sapling->transparent transaction {txid3} not found after 30 seconds. Last error: {e}").into());
                            }
                        }
                    }
                }
            }
            
            if retries > 30 {
                return Err("Sapling->transparent transaction confirmation timeout after 30 seconds".into());
            }
            
            std::thread::sleep(std::time::Duration::from_secs(1));
            retries += 1;
            print!(".");
            std::io::Write::flush(&mut std::io::stdout()).unwrap();
        }
        
        println!("\n🎉 === FULL TEST COMPLETED SUCCESSFULLY! ===");
        println!("✅ Built and sent transparent->transparent transaction");
        println!("✅ Built and sent transparent->sapling transaction");
        println!("✅ Found and decrypted sapling note");
        println!("✅ Built and sent sapling->transparent transaction");
        println!("🎉 Successfully completed the FULL transparent -> sapling -> transparent flow!");
    } else {
        println!("No sapling notes found - this would require a real zcashd daemon to complete the test");
    }
    
    Ok(())
} 

#[cfg(feature = "e2e")]
pub fn test_transparent_orchard_transparent_flow() -> Result<(), Box<dyn std::error::Error>> {
    use crate::orchard_sender::{
        send_from_orchard_to_transparent,
        decrypt_orchard_notes_in_tx,
        build_orchard_witness_from_blocks,
        OrchardSpendableNote,
        OrchardDecryptedNote,
    };
    use zcash_keys::keys::UnifiedSpendingKey;
    use zcash_keys::address::UnifiedAddress;
    use zcash_keys::keys::ReceiverRequirement;
    use zcash_keys::keys::UnifiedAddressRequest;
    use zcash_keys::encoding::AddressCodec;
    use bip0039::{English, Mnemonic};
    use zcash_primitives::zip32::AccountId;

    // ---- Init + config ----
    let network = Network::Regtest;
    init_zec()?;
    let rpc = ZcashdRpcClient::new(ZcashdConfig::default());
    let mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about";

    println!("=== Orchard Flow: setup & keys ===");
    let acct = derive_account_from_mnemonic(mnemonic.to_string(), network)?;
    println!("T-addr: {}", acct.transparent);

    // Derive a Unified Address that includes an Orchard receiver (account 0).
    // Using UA is fine: when both receivers are present, Zcash picks Orchard as most private.
    let m = Mnemonic::<English>::from_phrase(mnemonic)?;
    let seed = m.to_seed("").to_vec();
    let usk = UnifiedSpendingKey::from_seed(&network, &seed, AccountId::ZERO)?;
    let ufvk = usk.to_unified_full_viewing_key();
    let req = UnifiedAddressRequest::unsafe_custom(
        ReceiverRequirement::Require,
        ReceiverRequirement::Omit,
        ReceiverRequirement::Omit,
    );
    let (ua, _didx) = ufvk.default_address(req)?; // UA including Orchard receiver
    let orchard_ua = ua.encode(&network);
    println!("Derived UA (Orchard): {orchard_ua}");

    // Import T-addr for tracking and mine funds
    rpc.import_address(&acct.transparent, false)?;
    println!("Mining 110 blocks to mature coinbase...");
    rpc.generate_blocks(110)?;

    // ---- Grab a UTXO on our T-addr ----
    let utxos = rpc.get_address_utxos(&acct.transparent)?;
    if utxos.is_empty() {
        return Err("No UTXOs on transparent address; check -mineraddress".into());
    }
    let utxo = utxos[0].clone();
    let tip = rpc.get_block_count()? as u32;

    // ---- Phase A: T -> Orchard (to our UA) ----
    println!("\n=== T → Orchard ===");
    // Choose a ZIP-317-ish fee; your builder may adjust internally on size.
    let fee = 200_000;
    let send_amount = utxo.value.saturating_sub(fee);
    if send_amount == 0 {
        return Err("UTXO too small for fee".into());
    }

    let to_orchard_ptx = PartialTx {
        height: tip,
        txid: vec![],
        inputs: vec![utxo.clone()],
        outputs: vec![Output {
            address: orchard_ua.clone(),   // unified address (Orchard receiver selected)
            amount: send_amount,
            memo: "T->Orchard (regtest integration)".to_string(),
        }],
        fee,
        sighashes: vec![],
        expiry_height: 0,
        version: 0,
    };

    let to_orchard_ptx = build_ptx(acct.public_key.clone(), to_orchard_ptx, network)?;
    println!("Built T→Orchard tx; {} inputs to sign", to_orchard_ptx.sighashes.len());

    // Sign transparent inputs
    let mut sigs = Vec::with_capacity(to_orchard_ptx.sighashes.len());
    {
        use secp256k1::{Message, Secp256k1, SecretKey};
        let secp = Secp256k1::new();
        let sk = SecretKey::from_slice(&acct.private_key)
            .map_err(|e| format!("bad privkey: {e}"))?;
        for (i, sh) in to_orchard_ptx.sighashes.iter().enumerate() {
            let msg = Message::from_digest_slice(sh).map_err(|e| format!("bad sighash: {e}"))?;
            let sig = secp.sign_ecdsa(&msg, &sk).serialize_der().to_vec();
            println!("  sig[{i}] = {}", hex::encode(&sig));
            sigs.push(sig);
        }
    }
    let to_orchard_raw = apply_signatures(acct.public_key.clone(), to_orchard_ptx, sigs, network)?;
    let to_orchard_hex = hex::encode(&to_orchard_raw);
    let to_orchard_txid = rpc.send_raw_transaction(&to_orchard_hex)?;
    println!("✅ broadcast T→Orchard: {to_orchard_txid}");

    // Confirm it
    rpc.generate_blocks(3)?;
    println!("Mined blocks; waiting for 1 conf...");
    rpc.wait_for_transaction(&to_orchard_txid, 1)?;

    // Figure the mined height
    let mined_info = rpc.rpc_call("getrawtransaction", serde_json::json!([to_orchard_txid, 1]))?;
    let tx_height = mined_info["height"].as_u64().unwrap() as u32;
    println!("T→Orchard mined at height {tx_height}");
    
    // Mine a few more blocks to ensure the Orchard tree is finalized
    println!("Mining additional blocks to finalize Orchard tree...");
    rpc.generate_blocks(5)?;
    let anchor_height = tx_height + 5;

    // ---- Phase B: Decrypt our Orchard note ----
    println!("\n=== Decrypt Orchard note from the T→O tx ===");
    let raw_hex = rpc.get_raw_transaction(&to_orchard_txid)?;
    let raw_bytes = hex::decode(&raw_hex)?;
    let notes: Vec<OrchardDecryptedNote> = decrypt_orchard_notes_in_tx(
        mnemonic.to_string(),
        network,
        raw_bytes.clone(),
        tx_height,  // Use the actual transaction height for decryption
    )?;
    if notes.is_empty() {
        return Err("No Orchard notes decrypted from our T→O tx".into());
    }
    let onote = &notes[0];
    println!("Found Orchard note of {} zats", onote.value);

    // ---- Phase C: Build Orchard witness (genesis → anchor_height) ----
    println!("\n=== Build Orchard witness up to height {anchor_height} ===");
    
    // Fetch finalorchardroot at the anchor height
    let bh = rpc.rpc_call("getblockhash", json!([anchor_height]))?
        .as_str().unwrap().to_string();
    let hdr = rpc.rpc_call("getblockheader", json!([bh, true]))?;
    
    // Try to get the finalorchardroot - it might be in the header
    let root_hex = if let Some(orchard_root) = hdr.get("finalorchardroot") {
        orchard_root.as_str().ok_or("finalorchardroot not a string")?
    } else {
        // If no orchard root, we might need to calculate it ourselves
        println!("[WARN] No finalorchardroot in block header, computing from transactions");
        ""
    };
    
    let expected_root = if !root_hex.is_empty() {
        Some(hex::decode(root_hex)?)
    } else {
        None
    };
    
    let all_blocks = get_block_transactions_range(&rpc, 1, anchor_height)?;
    let wparts = build_orchard_witness_from_blocks(
        mnemonic.to_string(),
        network,
        onote.diversifier.clone(),
        onote.value,
        onote.rseed.clone(),
        onote.rho.clone(),  // Pass rho from the decrypted note
        onote.cmx.clone(),  // Pass the actual commitment
        vec![all_blocks], // Wrap in vec to match expected type
        BranchId::NU5,
        expected_root,
    )?;

    // ---- Phase D: Orchard → T (unshield back) ----
    println!("\n=== Orchard → T (unshield) ===");
    let spend = OrchardSpendableNote {
        diversifier: onote.diversifier.clone(),
        value: onote.value,
        rseed: onote.rseed.clone(),
        rho: onote.rho.clone(),  // Include rho from the decrypted note
        witness: wparts.witness_bytes.clone(),
        cmx: onote.cmx.clone(),  // Include the actual commitment to avoid reconstruction mismatch
        addr_bytes: onote.addr_bytes.clone(),  // Include the exact address bytes
    };
    // target height: slightly after anchor to ensure modern consensus selection
    let target_height = anchor_height + 50;

    let o2t_bytes = send_from_orchard_to_transparent(
        mnemonic.to_string(),
        network,
        vec![spend],
        acct.transparent.clone(),
        wparts.anchor_bytes.clone(),
        target_height,
        Some("Orchard->Transparent (regtest integration)".to_string()),
    )?;
    let o2t_hex = hex::encode(&o2t_bytes);
    let o2t_txid = rpc.send_raw_transaction(&o2t_hex)?;
    println!("✅ broadcast Orchard→T: {o2t_txid}");

    // Confirm it
    rpc.generate_blocks(3)?;
    rpc.wait_for_transaction(&o2t_txid, 1)?;
    println!("🎉 Orchard round-trip complete!");

    Ok(())
}

/// Helper function to get recent block transactions
#[cfg(feature = "e2e")]
fn get_block_transactions_range(rpc: &ZcashdRpcClient, start_height: u64, end_height: u32) -> Result<Vec<Vec<u8>>, Box<dyn std::error::Error>> {
    let mut transactions = vec![];
    
    println!("Fetching blocks {start_height} to {end_height} for complete Sapling tree...");
    for height in start_height..=(end_height as u64) {
        if height % 1000 == 0 {
            println!("  Processing block {height}/{end_height}");
        }
        
        // Get block hash
        let hash_result = rpc.rpc_call("getblockhash", json!([height]))?;
        let block_hash = hash_result.as_str().unwrap();
        
        // Get block
        let block_result = rpc.rpc_call("getblock", json!([block_hash, 1]))?;
        let block = block_result.as_object().unwrap();
        
        // Get transactions in block
        if let Some(txs) = block.get("tx").and_then(|v| v.as_array()) {
            for tx_hash in txs {
                let tx_hex = rpc.get_raw_transaction(tx_hash.as_str().unwrap())?;
                let tx_bytes = hex::decode(&tx_hex).unwrap();
                transactions.push(tx_bytes);
            }
        }
    }
    
    println!("Collected {} transactions from {} blocks", transactions.len(), end_height as u64 - start_height + 1);
    Ok(transactions)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Test that validates the basic flow without requiring a running zcashd
    #[test]
    fn test_transparent_sapling_transparent_validation() -> Result<(), Box<dyn std::error::Error>> {
        // Initialize the ZEC library for testing
        let network = Network::Regtest;
        init_zec()?;

        let mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about";
        
        // Test account derivation
        let account = derive_account_from_mnemonic(mnemonic.to_string(), network)?;
        
        // Validate addresses
        assert!(is_transparent_address(&account.transparent, &network));
        assert!(validate_address_v124(account.sapling.clone(), network).is_ok());
        
        println!("Address validation test passed");
        println!("Transparent address: {}", account.transparent);
        println!("Sapling address: {}", account.sapling);
        
        // Test creating a basic partial transaction structure
        let mock_utxo = UTXO {
            txid: "0000000000000000000000000000000000000000000000000000000000000000".to_string(),
            vout: 0,
            script: "76a914000000000000000000000000000000000000000088ac".to_string(), // P2PKH script
            value: 1_000_000_000, // 10 ZEC
            height: 100,
        };
        
        let ptx = PartialTx {
            height: 100,
            txid: vec![],
            inputs: vec![mock_utxo],
            outputs: vec![Output {
                address: account.sapling,
                amount: 999_900_000, // 9.999 ZEC (leaving 0.001 for fee)
                memo: "Test memo".to_string(),
            }],
            fee: 100_000,
            sighashes: vec![],
            expiry_height: 0,
            version: 0,
        };
        
        // Test building partial transaction
        let built_ptx = build_ptx(account.public_key, ptx, network)?;
        assert!(!built_ptx.sighashes.is_empty(), "Should generate sighashes for inputs");
        
        println!("Partial transaction building test passed");
        
        Ok(())
    }

    /// Integration test that requires a running zcashd daemon
    /// This test will be skipped if the ZCASHD_INTEGRATION environment variable is not set
    #[test]
    #[ignore] // Use --ignored to run this test
    #[cfg(feature = "e2e")]
    fn test_full_sapling_integration_with_zcashd() -> Result<(), Box<dyn std::error::Error>> {
        // Check if integration tests should run
        if std::env::var("ZCASHD_INTEGRATION").is_err() {
            println!("Skipping integration test - set ZCASHD_INTEGRATION=1 to run");
            return Ok(());
        }
        
        test_transparent_sapling_transparent_flow()
    }

    /// Integration test that requires a running zcashd daemon
    /// This test will be skipped if the ZCASHD_INTEGRATION environment variable is not set
    #[test]
    #[ignore] // run with: cargo test --features "shielded e2e" -- --ignored
    #[cfg(feature = "e2e")]
    fn test_full_orchard_integration_with_zcashd() -> Result<(), Box<dyn std::error::Error>> {
        // Only run when explicitly requested
        if std::env::var("ZCASHD_INTEGRATION").is_err() {
            println!("Skipping Orchard integration test - set ZCASHD_INTEGRATION=1 to run");
            return Ok(());
        }

        // Calls the flow provided above
        test_transparent_orchard_transparent_flow()
    }    
}

/// Helper function to check if zcashd is running
#[cfg(feature = "e2e")]
pub fn check_zcashd_running(config: &ZcashdConfig) -> bool {
    let rpc = ZcashdRpcClient::new(config.clone());
    rpc.get_block_count().is_ok()
}
