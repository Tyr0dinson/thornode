use std::path::PathBuf;

use anyhow::{Context, Result};
use ciphersuite::group::ff::{Field, PrimeField};
use ciphersuite::group::{Group, GroupEncoding};
use dalek_ff_group::{EdwardsPoint, Scalar};
use dkg_pedpop::ThresholdKeys;

type Ed25519 = dalek_ff_group::Ed25519;

fn main() -> Result<()> {
    let args: Vec<String> = std::env::args().collect();
    if args.len() < 3 {
        eprintln!("Usage: frost-combine <dir> <dir> [...]");
        eprintln!();
        eprintln!("Each argument is a sled data directory containing plaintext .frost key files.");
        std::process::exit(1);
    }

    let mut shares: Vec<(u16, Scalar)> = Vec::new();
    let mut group_key_bytes: Option<Vec<u8>> = None;

    for dir_str in &args[1..] {
        let data_dir = PathBuf::from(dir_str);
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
                let raw_bytes = std::fs::read(&path)?;

                let keys = match ThresholdKeys::<Ed25519>::read(&mut raw_bytes.as_slice()) {
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

    println!("spend_key: {}", hex::encode(full_secret.to_repr().as_ref()));

    let view_scalar = {
        use sha2::{Sha512, Digest};
        let mut hasher = Sha512::new();
        hasher.update(b"THORChain View Key");
        hasher.update(0u64.to_le_bytes());
        let digest: [u8; 64] = hasher.finalize().into();
        use curve25519_dalek::scalar::Scalar as DalekScalar;
        DalekScalar::from_bytes_mod_order_wide(&digest)
    };
    println!("view_key:  {}", hex::encode(view_scalar.as_bytes()));

    Ok(())
}
