from typing import Any, Dict, Callable, Optional, Set

import base64
import hashlib
import json

from libthorscan import libthorscan as thorscan


########################################################################################
# Internal Helpers
########################################################################################


def _print(ret: Optional[str]):
    """
    Print the provided value if it is not None.
    """
    if ret is not None:
        print(json.dumps(ret, indent=2))


def _event_dict(event: Dict[str, Any]) -> Dict[str, str]:
    """
    Convert base64 encoded key-value pairs into a dictionary.
    """
    e = {"type": event["type"]}
    for a in event["attributes"]:
        if a["value"] is None:
            continue
        key = base64.b64decode(a["key"]).decode("utf-8")
        e[key] = base64.b64decode(a["value"]).decode("utf-8")
    return e


def _gen_tx_msgs_listen(
    *listeners: Callable[[int, str, Dict[str, Any]], Optional[str]],
    types: Optional[Set[str]] = None,
):
    def _listen(height, txhash, tx):
        for msg in tx["body"]["messages"]:

            # simplify the type format
            msg["type"] = msg["@type"][7:]
            del msg["@type"]

            if types is not None and msg["type"] not in types:
                continue

            for l in listeners:
                _print(l(height, txhash, msg))

    return _listen


########################################################################################
# Scan
########################################################################################


def scan_blocks(
    start: int, end: int, *listeners: Callable[[Dict[str, Any]], Optional[str]]
):
    """
    Scan blocks from a start height.

    Args:
        start: height to start scan
        end: height to end scan
        listeners: listener functions accepting the block event (block)
    """
    thorscan.lib.ScanBlocks(start)

    while True:
        _b = thorscan.lib.GetScanBlock()
        b = json.loads(thorscan.ffi.string(_b).decode("utf-8"))
        thorscan.lib.Free(_b)

        for l in listeners:
            l(b)

        if b["block"]["header"]["height"] >= end:
            break


def scan_block_events(
    start: int,
    end: int,
    *listeners: Callable[[int, Dict[str, Any]], Optional[str]],
    types: Optional[Set[str]] = None,
):
    """
    Scan end block events from a start height.

    Args:
        start: height to start scan
        end: height to end scan
        listeners: listener functions accepting (height, event)
        types: event types to filter
    """
    thorscan.lib.ScanBlockResults(start)

    while True:
        _br = thorscan.lib.GetScanBlockResult()
        br = json.loads(thorscan.ffi.string(_br).decode("utf-8"))
        thorscan.lib.Free(_br)

        for event in br["begin_block_events"] or []:
            if types is not None and event["type"] not in types:
                continue
            for l in listeners:
                _print(l(br["height"], _event_dict(event)))
        for event in br["end_block_events"] or []:
            if types is not None and event["type"] not in types:
                continue
            for l in listeners:
                _print(l(br["height"], _event_dict(event)))

        if br["height"] >= end:
            break


def scan_txs(
    start: int,
    end: int,
    *listeners: Callable[[int, str, Dict[str, Any], Dict[str, Any]], Optional[str]],
    successful: bool = True,
    failed: bool = True,
    result: bool = False,
):
    """
    Scan transactions from a start height.

    Args:
        start: height to start scan
        end: height to end scan
        listeners: listener functions accepting (height, txhash, tx, result)
        successful: scan successful transactions
        failed: scan failed transactions
        result: include the transaction result
    """

    if not successful or not failed or result:
        thorscan.lib.ScanBlockResults(start)

    def _listen(block):
        filtered_results = []
        if not successful or not failed:
            _br = thorscan.lib.GetScanBlockResult()
            br = json.loads(thorscan.ffi.string(_br).decode("utf-8"))
            thorscan.lib.Free(_br)

            filtered = []
            for i, txr in enumerate(br["txs_results"] or []):
                if (txr["code"] == 0 and successful) or (txr["code"] != 0 and failed):
                    filtered_results.append(txr)
                    filtered.append(block["block"]["data"]["txs"][i])
            block["block"]["data"]["txs"] = filtered

        for i, txb in enumerate(block["block"]["data"]["txs"] or []):
            _tx = thorscan.lib.TxToJSON(bytes(txb.encode("utf-8")))
            tx = json.loads(thorscan.ffi.string(_tx).decode("utf-8"))
            thorscan.lib.Free(_tx)

            for l in listeners:
                _print(
                    l(
                        block["block"]["header"]["height"],
                        hashlib.sha256(base64.b64decode(txb)).hexdigest().upper(),
                        tx,
                        filtered_results[i],
                    )
                )

    scan_blocks(start, end, _listen)


def scan_tx_events(
    start: int,
    end: int,
    *listeners: Callable[[int, Dict[str, Any], Dict[str, str]], Optional[str]],
    types: Optional[Set[str]] = None,
    actions: Optional[Set[str]] = None,
):
    """
    Scan transaction events from a start height.

    Args:
        start: height to start scan
        end: height to end scan
        listeners: listener functions accepting (height, tx_result, event)
        types: event types to filter
        actions: event actions to filter
    """
    thorscan.lib.ScanBlockResults(start)

    while True:
        _br = thorscan.lib.GetScanBlockResult()
        br = json.loads(thorscan.ffi.string(_br).decode("utf-8"))
        thorscan.lib.Free(_br)

        for tx in br["txs_results"] or []:
            for event in tx.get("events", []):
                event = _event_dict(event)
                if types is not None and event["type"] not in types:
                    continue
                if actions is not None and event.get("action", "") not in actions:
                    continue
                for l in listeners:
                    _print(l(br["height"], tx, event))

        if br["height"] >= end:
            break


def scan_msgs(
    start: int,
    end: int,
    *listeners: Callable[[int, str, Dict[str, Any]], Optional[str]],
    types: Optional[Set[str]] = None,
):
    """
    Scan transaction messages from a start height.

    Args:
        start: height to start scan
        end: height to end scan
        listeners: listener functions accepting (height, txhash, msg)
        types: message types to filter
    """
    scan_txs(start, end, _gen_tx_msgs_listen(*listeners, types=types))


########################################################################################
# Subscribe
########################################################################################


def subscribe_blocks(*listeners: Callable[[Dict[str, Any]], Optional[str]]):
    """
    Subscribe to new blocks.

    Args:
        listeners: listener functions accepting the block event (block)
    """
    thorscan.lib.SubscribeBlocks()

    while True:
        _b = thorscan.lib.GetSubscribeBlock()
        b = json.loads(thorscan.ffi.string(_b).decode("utf-8"))
        thorscan.lib.Free(_b)

        for l in listeners:
            _print(l(b))


def subscribe_block_events(*listeners: Callable[[int, Dict[str, Any]], Optional[str]]):
    """
    Subscribe to new end block events.

    Args:
        listeners: listener functions accepting (height, event)
    """

    def _listen(block):
        for event in block["result_end_block"]["events"]:
            for l in listeners:
                _print(l(block["block"]["header"]["height"], _event_dict(event)))

    subscribe_blocks(_listen)


def subscribe_txs(
    *listeners: Callable[[int, str, Dict[str, Any], Dict[str, Any]], Optional[str]],
):
    """
    Subscribe to new transactions.

    Args:
        listeners: listener functions accepting (height, txhash, tx, result)
    """
    thorscan.lib.SubscribeTxs()

    while True:
        _stx = thorscan.lib.GetSubscribeTx()
        stx = json.loads(thorscan.ffi.string(_stx).decode("utf-8"))
        thorscan.lib.Free(_stx)

        _tx = thorscan.lib.TxToJSON(bytes(stx["tx"].encode("utf-8")))
        tx = json.loads(thorscan.ffi.string(_tx).decode("utf-8"))
        thorscan.lib.Free(_tx)

        for l in listeners:
            _print(
                l(
                    stx["height"],
                    hashlib.sha256(base64.b64decode(stx["tx"])).hexdigest().upper(),
                    tx,
                    stx["result"],
                )
            )


def subscribe_tx_events(
    *listeners: Callable[
        [int, str, Dict[str, Any], Dict[str, Any], Dict[str, str]], Optional[str]
    ],
    types: Optional[Set[str]] = None,
    actions: Optional[Set[str]] = None,
):
    """
    Subscribe to new tx events.

    Args:
        listeners: listener functions accepting (height, txhash, tx, result, event)
    """

    def _listen(height, txhash, tx, result):
        for event in result["events"]:
            event = _event_dict(event)
            if types is not None and event["type"] not in types:
                continue
            if actions is not None and event.get("action", "") not in actions:
                continue
            for l in listeners:
                _print(l(height, txhash, tx, result, event))

    subscribe_txs(_listen)


def subscribe_msgs(
    *listeners: Callable[[int, str, Dict[str, Any]], Optional[str]],
    types: Optional[Set[str]] = None,
):
    """
    Subscribe to new transaction messages.

    Args:
        listeners: listener functions accepting (height, txhash, msg)
        types: message types to filter
    """

    _listen = _gen_tx_msgs_listen(*listeners, types=types)

    def __listen(height, txhash, tx, _):
        _listen(height, txhash, tx)

    subscribe_txs(__listen)
