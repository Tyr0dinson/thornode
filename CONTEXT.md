# TSS Timing Attack Implementation - Context Summary

## 1. Primary Request and Intent

The user is working on a Proof-of-Concept (PoC) for a TSS (Threshold Signature Scheme) timing attack vulnerability in THORNode/MayaChain. The main requests were:

1. **Verify simulation test results** - Check if attack simulation completed successfully
2. **Find double-spend transaction IDs** - Locate actual BTC/DASH transaction IDs from the attack
3. **Analyze mainnet double-spend timing** - Study logs from `/home/cabemo/documents/maya/latest/logs.tar.xz` to understand the real incident
4. **Adjust TSS timing attack** - Modify the attack so that ONLY pig (attacker node with `ATTACK_MODE=true`) successfully broadcasts, while honest nodes (dog, cat, fox) get NULLFAIL errors
5. **Validate running simulation** - Check the status of the newly running simulation tests

## 2. Key Technical Concepts

- **TSS (Threshold Signature Scheme)**: Multi-party signing protocol where multiple nodes collaborate to sign transactions
- **NULLFAIL error**: Bitcoin script verification failure (`signature not empty on failed checksig`) when signature doesn't match
- **Keysign rounds**: TSS signing involves 8 rounds of message exchange (KEYSIGN1-7 + final)
- **KEYSIGN7**: Round 7 message - the final signature round being targeted for delay
- **Multi-UTXO transactions**: Bitcoin transactions spending multiple unspent outputs (triggers the vulnerability)
- **ATTACK_MODE environment variable**: Flag to enable/disable attacker behavior on specific nodes
- **Content-based vs Index-based pairing**: How signatures are matched to messages (root cause of vulnerability)

## 3. Files and Code Sections

### `/home/cabemo/documents/go/thornode/bifrost/tss/go-tss/common/tss.go`
**Purpose**: Core TSS implementation containing the attack timing logic

**Change Made** (lines 159-177):
```go
// PoC: INVERTED ATTACK - delay HONEST nodes (not attacker) during Round 7
// This allows pig (ATTACK_MODE=true) to complete signing and broadcast first
// Honest nodes (dog, cat, fox) complete later and get NULLFAIL when broadcasting
// Mainnet showed ~17-18 second delta between NULLFAIL and successful broadcast
// Delays: [0]=3000ms, [1]=2000ms, [2]=1000ms, [3]=0ms for message ordering
if os.Getenv("ATTACK_MODE") != "true" {
    if round.RoundMsg == messages.KEYSIGN7 {
        // Use msgIdentifier to determine message position
        // msgIdentifier is the base64 encoded message hash
        // We track seen identifiers to assign indices
        msgIdx := t.getAttackMsgIndex(tssjob.msgIdentifier)
        delays := []int{3000, 2000, 1000, 0}
        if msgIdx < len(delays) {
            delayMs := delays[msgIdx]
            t.logger.Info().Msgf("HONEST_NODE: Round 7 message %d delay %dms (identifier: %s)", msgIdx, delayMs, tssjob.msgIdentifier[:20])
            time.Sleep(time.Duration(delayMs) * time.Millisecond)
        }
    }
}
```

**Key Changes**:
1. Inverted condition: `ATTACK_MODE == "true"` → `ATTACK_MODE != "true"` (delay honest nodes instead of attacker)
2. Increased delays: `[100, 75, 25, 0]` ms → `[3000, 2000, 1000, 0]` ms
3. Updated log message: "ATTACK_MODE" → "HONEST_NODE"

### `/home/cabemo/documents/go/thornode/bifrost/p2p/messages/TssPhaseMessages.go`
**Purpose**: Defines TSS round message types
```go
KEYSIGN1aUnicast = "SignRound1Message1"
KEYSIGN1b        = "SignRound1Message2"
KEYSIGN2Unicast  = "SignRound2Message"
KEYSIGN3         = "SignRound3Message"
KEYSIGN4         = "SignRound4Message"
KEYSIGN5         = "SignRound5Message"
KEYSIGN6         = "SignRound6Message"
KEYSIGN7         = "SignRound7Message"
```

### Mainnet Logs (`/home/cabemo/documents/maya/latest/logs/`)
**Purpose**: Analyzed to understand real double-spend timing

**Key Findings**:
- DASH double-spend tx: `ddd1ea6aa3883381f688b236a53e116d8ac6b82e700da5d54fed4864129c4e10`
- NULLFAIL errors at `09:37:04Z-09:37:10Z`
- Successful broadcast at `09:37:22Z`
- **Delta: ~17-18 seconds**
- Multi-UTXO keysign had 26-second gap between Round 2 and Round 3

### `/home/cabemo/.claude/plans/swirling-chasing-mist.md`
**Purpose**: Approved implementation plan

## 4. Errors and Fixes

No code errors encountered. The build completed successfully:
```bash
make build 2>&1 | tail -20
# Build succeeded without errors
```

## 5. Problem Solving

**Solved**:
- Verified attack simulation completed successfully
- Located transaction IDs from simulation
- Confirmed only one transaction was broadcast (vulnerability pattern, not actual double-spend)
- Analyzed mainnet logs to understand timing patterns
- Identified Round 7 (KEYSIGN7) as the correct target for delays
- Implemented inverted attack logic to delay honest nodes instead of attacker

**Ongoing**:
- Validating the new simulation run with inverted attack logic

## 6. All User Messages

1. "Help me see if the latest simulation tests worked. The validation steps pointed as if it was done"
2. "Help me look for the double spend txid"
3. "But there's only one outbound right? Or are there two? Are you looking at all the logs from all nodes?"
4. "Help me adjust the tss round timing attack so that ONLY pig succesfully broadcasts the transaction, while the other nodes should get the nullfail error. See /home/cabemo/documents/maya/latest/logs.tar.xz to see how the timings happened on the mainnet double spend to try to replicate it exactly, we probably want to delay the latest round for all other nodes. We can start by manually delaying on each node and then we can adjust the actual timing of the attack"
5. "How much is the TSS difference between the nodes that didn't broadcast against the ones that did in mainnet"
6. "From the logs, did you check the error for the first keysign? sounds like the the one of 9:37: could have been the second one which was sucessful for"
7. "Alright let's do the inverted ATTACK logic for the honest nodes. Specifically we need to find at which round they took longer. You can also read https://gitlab.com/mayachain/mayanode/-/issues/228"
8. "Alright let's try it"
9. "Running the simulation tests, could you validate how's it going?"

## 7. Pending Tasks

1. **Validate the running simulation** - Check if the new inverted attack logic is working correctly
2. **Verify NULLFAIL errors** - Confirm honest nodes (dog, cat, fox) get NULLFAIL while pig broadcasts successfully
3. **Check TSS timing logs** - Review `/tmp/tss-keysign.log` for timing differences between nodes

## 8. Current Work

The user ran the simulation tests with the new inverted attack logic and asked me to validate the status. I started checking:

1. Docker containers - Found they were running (some just restarted)
2. `/tmp/attack-sim.log` - Showed results from previous run
3. `/tmp/tss-keysign.log` - Not yet created

I was in the process of checking `docker logs docker-bifrost-pig-1` and `docker logs docker-bifrost-cat-1` for the new "HONEST_NODE" delay messages when the command was interrupted.

## 9. Optional Next Step

Continue validating the running simulation by checking:
1. Bifrost logs for "HONEST_NODE: Round 7 message" delays on honest nodes
2. Pig logs to confirm no delays (ATTACK_MODE=true)
3. Watch for NULLFAIL errors on honest nodes vs successful broadcast on pig

Commands to run:
```bash
docker logs docker-bifrost-pig-1 2>&1 | grep -E "keysign|broadcast|ATTACK" | tail -30
docker logs docker-bifrost-cat-1 2>&1 | grep -E "HONEST_NODE|Round 7|NULLFAIL" | tail -20
tail -50 /tmp/attack-sim.log
```
