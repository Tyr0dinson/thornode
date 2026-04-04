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
        eprintln!("Usage: frost-combine <sled_dir_1:secret_1> <sled_dir_2:secret_2> [...]");
        eprintln!();
        eprintln!("Each argument is a sled data directory and its SIGNER_SEED_PHRASE,");
        eprintln!("separated by colon. Need t shares for a t-of-n threshold.");
        eprintln!();
        eprintln!("Example:");
        eprintln!("  frost-combine \\");
        eprintln!("    '/root/frost-backup/xmr-frost-signer:dog dog ... fossil' \\");
        eprintln!("    '/root/frost-backup/xmr-frost-signer-cat:cat cat ... crawl'");
        std::process::exit(1);
    }

    let mut shares: Vec<(u16, Scalar)> = Vec::new();
    let mut group_key_bytes: Option<Vec<u8>> = None;

    for arg in &args[1..] {
        let (dir_str, secret) = arg.split_once(':').context(
            "each argument must be sled_dir:seed_phrase separated by colon",
        )?;
        let data_dir = PathBuf::from(dir_str);

        let store = xmr_frost_signer::state::Store::open(&data_dir, secret)?;
        let vaults = store.list_vaults()?;

        if vaults.is_empty() {
            eprintln!("warning: no vaults in {}", dir_str);
            continue;
        }

        for vault in &vaults {
            let key_bytes = match store.load_frost_keys(&vault.vault_pub_key)? {
                Some(b) => b,
                None => {
                    eprintln!("  skip vault {} (no frost keys)", vault.vault_pub_key);
                    continue;
                }
            };

            let keys = ThresholdKeys::<Ed25519>::read(&mut key_bytes.as_slice())
                .map_err(|e| anyhow::anyhow!("failed to deserialize keys: {e}"))?;

            let params = keys.params();
            let participant_idx = u16::from(params.i());
            let share = keys.original_secret_share();
            let gk = keys.group_key();
            let gk_bytes = gk.to_bytes();

            eprintln!(
                "loaded: vault={} participant={} threshold={}/{} group_key={}",
                vault.vault_pub_key,
                participant_idx,
                params.t(),
                params.n(),
                hex::encode(gk_bytes.as_ref()),
            );

            if let Some(ref existing) = group_key_bytes {
                if gk_bytes.as_ref() != existing.as_slice() {
                    anyhow::bail!("group key mismatch between shares");
                }
            } else {
                group_key_bytes = Some(gk_bytes.as_ref().to_vec());
            }

            shares.push((participant_idx, *share.clone()));
        }
    }

    if shares.len() < 2 {
        anyhow::bail!("need at least 2 shares for interpolation, got {}", shares.len());
    }

    eprintln!("interpolating {} shares...", shares.len());

    // Lagrange interpolation at x=0 to reconstruct the full secret
    let xs: Vec<Scalar> = shares.iter().map(|(idx, _)| Scalar::from(u64::from(*idx))).collect();

    let mut full_secret = Scalar::ZERO;
    for (i, (_, share_i)) in shares.iter().enumerate() {
        let mut coeff = Scalar::ONE;
        for (j, _) in shares.iter().enumerate() {
            if i == j {
                continue;
            }
            let neg_xj = -xs[j];
            let diff = xs[i] - xs[j];
            let inv: Scalar = Option::from(diff.invert()).context("failed to invert — duplicate participant index?")?;
            coeff = coeff * neg_xj * inv;
        }
        full_secret = full_secret + *share_i * coeff;
    }

    // Verify against group key
    let reconstructed_pub = EdwardsPoint::generator() * full_secret;
    let reconstructed_bytes = reconstructed_pub.to_bytes();
    if let Some(ref expected) = group_key_bytes {
        if reconstructed_bytes.as_ref() == expected.as_slice() {
            eprintln!("verified: reconstructed public key matches group key");
        } else {
            eprintln!("WARNING: public key mismatch!");
            eprintln!("  got:      {}", hex::encode(reconstructed_bytes.as_ref()));
            eprintln!("  expected: {}", hex::encode(expected));
        }
    }

    let secret_repr = full_secret.to_repr();
    println!("{}", hex::encode(secret_repr.as_ref()));

    Ok(())
}
