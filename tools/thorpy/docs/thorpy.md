<!-- markdownlint-disable -->

<a href="../../../tools/thorpy/thorpy.py#L0"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

# <kbd>module</kbd> `thorpy`

---

<a href="../../../tools/thorpy/thorpy.py#L68"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `scan_blocks`

```python
scan_blocks(
    start: int,
    end: int,
    *listeners: Callable[[Dict[str, Any]], Optional[str]]
)
```

Scan blocks from a start height.

**Args:**

- <b>`start`</b>: height to start scan
- <b>`end`</b>: height to end scan
- <b>`listeners`</b>: listener functions accepting the block event (block)

---

<a href="../../../tools/thorpy/thorpy.py#L93"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `scan_block_events`

```python
scan_block_events(
    start: int,
    end: int,
    *listeners: Callable[[int, Dict[str, Any]], Optional[str]],
    types: Optional[Set[str]] = None
)
```

Scan end block events from a start height.

**Args:**

- <b>`start`</b>: height to start scan
- <b>`end`</b>: height to end scan
- <b>`listeners`</b>: listener functions accepting (height, event)
- <b>`types`</b>: event types to filter

---

<a href="../../../tools/thorpy/thorpy.py#L130"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `scan_txs`

```python
scan_txs(
    start: int,
    end: int,
    *listeners: Callable[[int, str, Dict[str, Any], Dict[str, Any]], Optional[str]],
    successful: bool = True,
    failed: bool = True,
    result: bool = False
)
```

Scan transactions from a start height.

**Args:**

- <b>`start`</b>: height to start scan
- <b>`end`</b>: height to end scan
- <b>`listeners`</b>: listener functions accepting (height, txhash, tx, result)
- <b>`successful`</b>: scan successful transactions
- <b>`failed`</b>: scan failed transactions
- <b>`result`</b>: include the transaction result

---

<a href="../../../tools/thorpy/thorpy.py#L185"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `scan_tx_events`

```python
scan_tx_events(
    start: int,
    end: int,
    *listeners: Callable[[int, Dict[str, Any], Dict[str, str]], Optional[str]],
    types: Optional[Set[str]] = None,
    actions: Optional[Set[str]] = None
)
```

Scan transaction events from a start height.

**Args:**

- <b>`start`</b>: height to start scan
- <b>`end`</b>: height to end scan
- <b>`listeners`</b>: listener functions accepting (height, tx_result, event)
- <b>`types`</b>: event types to filter
- <b>`actions`</b>: event actions to filter

---

<a href="../../../tools/thorpy/thorpy.py#L223"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `scan_msgs`

```python
scan_msgs(
    start: int,
    end: int,
    *listeners: Callable[[int, str, Dict[str, Any]], Optional[str]],
    types: Optional[Set[str]] = None
)
```

Scan transaction messages from a start height.

**Args:**

- <b>`start`</b>: height to start scan
- <b>`end`</b>: height to end scan
- <b>`listeners`</b>: listener functions accepting (height, txhash, msg)
- <b>`types`</b>: message types to filter

---

<a href="../../../tools/thorpy/thorpy.py#L246"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `subscribe_blocks`

```python
subscribe_blocks(*listeners: Callable[[Dict[str, Any]], Optional[str]])
```

Subscribe to new blocks.

**Args:**

- <b>`listeners`</b>: listener functions accepting the block event (block)

---

<a href="../../../tools/thorpy/thorpy.py#L264"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `subscribe_block_events`

```python
subscribe_block_events(
    *listeners: Callable[[int, Dict[str, Any]], Optional[str]],
    types: Optional[Set[str]] = None
)
```

Subscribe to new end block events.

**Args:**

- <b>`listeners`</b>: listener functions accepting (height, event)
- <b>`types`</b>: event types to filter

---

<a href="../../../tools/thorpy/thorpy.py#L287"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `subscribe_txs`

```python
subscribe_txs(
    *listeners: Callable[[int, str, Dict[str, Any], Dict[str, Any]], Optional[str]]
)
```

Subscribe to new transactions.

**Args:**

- <b>`listeners`</b>: listener functions accepting (height, txhash, tx, result)

---

<a href="../../../tools/thorpy/thorpy.py#L318"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `subscribe_tx_events`

```python
subscribe_tx_events(
    *listeners: Callable[[int, str, Dict[str, Any], Dict[str, Any], Dict[str, str]], Optional[str]],
    types: Optional[Set[str]] = None,
    actions: Optional[Set[str]] = None
)
```

Subscribe to new tx events.

**Args:**

- <b>`listeners`</b>: listener functions accepting (height, txhash, tx, result, event)

---

<a href="../../../tools/thorpy/thorpy.py#L345"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `subscribe_msgs`

```python
subscribe_msgs(
    *listeners: Callable[[int, str, Dict[str, Any]], Optional[str]],
    types: Optional[Set[str]] = None
)
```

Subscribe to new transaction messages.

**Args:**

- <b>`listeners`</b>: listener functions accepting (height, txhash, msg)
- <b>`types`</b>: message types to filter

---

_This file was automatically generated via [lazydocs](https://github.com/ml-tooling/lazydocs)._
