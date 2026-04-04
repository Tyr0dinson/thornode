use std::path::PathBuf;

use anyhow::{Context, Result};
use ciphersuite::group::ff::{Field, PrimeField};
use ciphersuite::group::{Group, GroupEncoding};
use dalek_ff_group::{EdwardsPoint, Scalar};
use dkg_pedpop::ThresholdKeys;

type Ed25519 = dalek_ff_group::Ed25519;

fn derive_key(secret: &str, data_dir: &PathBuf) -> Result<[u8; 32]> {
    let metadata_path = data_dir.join("store_kdf.json");
    let raw = std::fs::read(&metadata_path)
        .with_context(|| format!("failed to read {}", metadata_path.display()))?;

    #[derive(serde::Deserialize)]
    struct KdfMeta { salt_hex: String, mem_kib: u32, time_cost: u32, parallelism: u32 }
    let meta: KdfMeta = serde_json::from_slice(&raw)?;

    let salt = hex::decode(&meta.salt_hex)?;
    let params = argon2::Params::new(meta.mem_kib, meta.time_cost, meta.parallelism, Some(32))
        .map_err(|e| anyhow::anyhow!("argon2 params: {e}"))?;
    let argon2 = argon2::Argon2::new(argon2::Algorithm::Argon2id, argon2::Version::V0x13, params);
    let mut key = [0u8; 32];
    argon2.hash_password_into(secret.as_bytes(), &salt, &mut key)
        .map_err(|e| anyhow::anyhow!("argon2 hash: {e}"))?;
    Ok(key)
}

fn decrypt_v2(key: &[u8; 32], data: &[u8]) -> Result<Vec<u8>> {
    use chacha20poly1305::{XChaCha20Poly1305, KeyInit, aead::Aead, XNonce};
    if data.len() < 28 || &data[..4] != b"XFS2" {
        anyhow::bail!("not a v2 encrypted file (missing XFS2 header)");
    }
    let nonce = XNonce::from_slice(&data[4..28]);
    let cipher = XChaCha20Poly1305::new(key.into());
    cipher.decrypt(nonce, &data[28..])
        .map_err(|_| anyhow::anyhow!("decrypt failed (wrong secret?)"))
}

fn main() -> Result<()> {
    let args: Vec<String> = std::env::args().collect();
    if args.len() < 3 {
        eprintln!("Usage: frost-combine [--raw-secret] <dir:secret> <dir:secret> [...]");
        eprintln!();
        eprintln!("Each argument is a sled data directory and its secret, separated by colon.");
        eprintln!("--raw-secret: secret is a raw XMR_FROST_STORE_SECRET");
        eprintln!("default: secret is SIGNER_SEED_PHRASE (derived via SHA256)");
        std::process::exit(1);
    }

    let raw_mode = args.iter().any(|a| a == "--raw-secret");
    let mut shares: Vec<(u16, Scalar)> = Vec::new();
    let mut group_key_bytes: Option<Vec<u8>> = None;

    for arg in &args[1..] {
        if arg == "--raw-secret" { continue; }
        let (dir_str, raw_input) = arg.split_once(':').context("format: dir:secret")?;
        let data_dir = PathBuf::from(dir_str);

        let store_secret = if raw_mode {
            raw_input.to_string()
        } else {
            use sha2::{Digest, Sha256};
            let mut h = Sha256::new();
            h.update(raw_input.as_bytes());
            h.update(b"xmr-frost-store");
            hex::encode(h.finalize())
        };

        let key = derive_key(&store_secret, &data_dir)?;
        let keys_dir = data_dir.join("keys");
        if !keys_dir.exists() {
            eprintln!("warning: no keys/ dir in {}", dir_str);
            continue;
        }

        for entry in std::fs::read_dir(&keys_dir)? {
            let entry = entry?;
            let path = entry.path();
            if path.extension().map(|e| e == "frost").unwrap_or(false) {
                let vault_id = path.file_stem().unwrap().to_string_lossy().to_string();
                let encrypted = std::fs::read(&path)?;
                let decrypted = match decrypt_v2(&key, &encrypted) {
                    Ok(d) => d,
                    Err(e) => {
                        eprintln!("  skip {} ({})", vault_id, e);
                        continue;
                    }
                };

                let keys = match ThresholdKeys::<Ed25519>::read(&mut decrypted.as_slice()) {
                    Ok(k) => k,
                    Err(e) => {
                        eprintln!("  skip {} (deserialize: {})", vault_id, e);
                        continue;
                    }
                };

                let params = keys.params();
                let idx = u16::from(params.i());
                let share = keys.original_secret_share();
                let gk = keys.group_key();
                let gk_hex = hex::encode(gk.to_bytes().as_ref());

                eprintln!("loaded: {} participant={} t={} n={} group_key={}", vault_id, idx, params.t(), params.n(), gk_hex);

                // Skip 1-of-1 bootstrap keys — we want the real threshold shares
                if params.t() == 1 && params.n() == 1 {
                    eprintln!("  (1-of-1 bootstrap key, skipping)");
                    continue;
                }

                if let Some(ref existing) = group_key_bytes {
                    if gk.to_bytes().as_ref() != existing.as_slice() {
                        eprintln!("  (different group key, skipping)");
                        continue;
                    }
                } else {
                    group_key_bytes = Some(gk.to_bytes().as_ref().to_vec());
                }

                shares.push((idx, *share.clone()));
            }
        }
    }

    if shares.len() < 2 {
        anyhow::bail!("need >= 2 shares, got {}", shares.len());
    }

    eprintln!("interpolating {} shares...", shares.len());

    let xs: Vec<Scalar> = shares.iter().map(|(i, _)| Scalar::from(u64::from(*i))).collect();
    let mut full_secret = Scalar::ZERO;
    for (i, (_, si)) in shares.iter().enumerate() {
        let mut coeff = Scalar::ONE;
        for (j, _) in shares.iter().enumerate() {
            if i == j { continue; }
            let inv: Scalar = Option::from((xs[i] - xs[j]).invert()).context("duplicate index")?;
            coeff = coeff * (-xs[j]) * inv;
        }
        full_secret = full_secret + *si * coeff;
    }

    let reconstructed = EdwardsPoint::generator() * full_secret;
    if let Some(ref expected) = group_key_bytes {
        if reconstructed.to_bytes().as_ref() == expected.as_slice() {
            eprintln!("verified: reconstructed key matches group key");
        } else {
            eprintln!("WARNING: key mismatch!");
            eprintln!("  got:      {}", hex::encode(reconstructed.to_bytes().as_ref()));
            eprintln!("  expected: {}", hex::encode(expected));
        }
    }

    println!("{}", hex::encode(full_secret.to_repr().as_ref()));
    Ok(())
}
