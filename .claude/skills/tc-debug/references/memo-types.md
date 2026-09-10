# Memo prefixes and expected pipeline

Parse the memo to determine the action type and adjust investigation:

| Memo prefix                 | Action                         | Key difference                                  |
| --------------------------- | ------------------------------ | ----------------------------------------------- |
| `SWAP:` or `=` or `s:`      | Swap                           | Full pipeline: observe → swap → outbound        |
| `ADD:` or `+` or `a:`       | Add liquidity                  | No outbound — completes at inbound finalisation |
| `WITHDRAW:` or `-` or `wd:` | Withdraw liquidity             | Outbound sends LP's share back                  |
| `DONATE:` or `d:`           | Donate to pool                 | No outbound                                     |
| `BOND:` or `UNBOND:`        | Node bond/unbond               | Bond has no outbound; unbond sends RUNE         |
| `LEAVE:`                    | Node leave                     | Sends bond back                                 |
| `MIGRATE:`                  | Vault migration                | Internal — skip in stuck outbound analysis      |
| `NOOP:`                     | No-op (gas)                    | Observed only, no action                        |
| `REFUND:`                   | Refund                         | Should have an outbound returning funds         |
| `$+` or `$-`                | Trade account deposit/withdraw | Deposit has no outbound; withdraw does          |
| `LOAN+:` or `LOAN-:`        | Loan open/close                | Open has outbound; close may not                |

For add/donate/bond with no expected outbound: if the stage shows completed at `inbound_finalised` and there are no planned outbounds, this is **normal** — not stuck.
