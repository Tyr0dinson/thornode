<!-- markdownlint-disable -->

<a href="../../../tools/thorpy/thorpy.py#L0"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

# <kbd>module</kbd> `thorpy`





---

<a href="../../../tools/thorpy/thorpy.py#L80"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `scan_blocks`

```python
scan_blocks(
    start: int,
    end: int,
    *listeners: Callable[[Dict[str, Any]], NoneType]
)
```

Scan blocks from a start height. 



**Args:**
 
 - <b>`start`</b>:  height to start scan 
 - <b>`end`</b>:  height to end scan 
 - <b>`listeners`</b>:  listener functions accepting the block event (block) 


---

<a href="../../../tools/thorpy/thorpy.py#L103"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `scan_block_events`

```python
scan_block_events(
    start: int,
    end: int,
    *listeners: Callable[[int, Dict[str, Any]], NoneType],
    types: Optional[Set[str]] = None
)
```

Scan end block events from a start height. 



**Args:**
 
 - <b>`start`</b>:  height to start scan 
 - <b>`end`</b>:  height to end scan 
 - <b>`listeners`</b>:  listener functions accepting (height, event) 
 - <b>`types`</b>:  event types to filter 


---

<a href="../../../tools/thorpy/thorpy.py#L140"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `scan_tx_events`

```python
scan_tx_events(
    start: int,
    end: int,
    *listeners: Callable[[int, Dict[str, Any], Dict[str, str]], NoneType],
    types: Optional[Set[str]] = None,
    actions: Optional[Set[str]] = None
)
```

Scan transaction events from a start height. 



**Args:**
 
 - <b>`start`</b>:  height to start scan 
 - <b>`end`</b>:  height to end scan 
 - <b>`listeners`</b>:  listener functions accepting (height, tx_result, event) 
 - <b>`types`</b>:  event types to filter 
 - <b>`actions`</b>:  event actions to filter 


---

<a href="../../../tools/thorpy/thorpy.py#L178"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `scan_txs`

```python
scan_txs(
    start: int,
    end: int,
    *listeners: Callable[[int, str, Dict[str, Any]], NoneType]
)
```

Scan transactions from a start height. 



**Args:**
 
 - <b>`start`</b>:  height to start scan 
 - <b>`end`</b>:  height to end scan 
 - <b>`listeners`</b>:  listener functions accepting (height, txhash, tx) 


---

<a href="../../../tools/thorpy/thorpy.py#L192"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `scan_msgs`

```python
scan_msgs(
    start: int,
    end: int,
    *listeners: Callable[[int, str, Dict[str, Any]], NoneType],
    types: Optional[Set[str]] = None
)
```

Scan transaction messages from a start height. 



**Args:**
 
 - <b>`start`</b>:  height to start scan 
 - <b>`end`</b>:  height to end scan 
 - <b>`listeners`</b>:  listener functions accepting (height, txhash, msg) 
 - <b>`types`</b>:  message types to filter 


---

<a href="../../../tools/thorpy/thorpy.py#L215"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `subscribe_blocks`

```python
subscribe_blocks(*listeners: Callable[[Dict[str, Any]], NoneType])
```

Subscribe to new blocks. 



**Args:**
 
 - <b>`listeners`</b>:  listener functions accepting the block event (block) 


---

<a href="../../../tools/thorpy/thorpy.py#L233"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `subscribe_block_events`

```python
subscribe_block_events(*listeners: Callable[[int, Dict[str, Any]], NoneType])
```

Subscribe to new end block events. 



**Args:**
 
 - <b>`listeners`</b>:  listener functions accepting (height, event) 


---

<a href="../../../tools/thorpy/thorpy.py#L249"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `subscribe_txs`

```python
subscribe_txs(
    *listeners: Callable[[int, str, Dict[str, Any], Dict[str, Any]], NoneType]
)
```

Subscribe to new transactions. 



**Args:**
 
 - <b>`listeners`</b>:  listener functions accepting (height, txhash, tx, result) 


---

<a href="../../../tools/thorpy/thorpy.py#L280"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `subscribe_tx_events`

```python
subscribe_tx_events(
    *listeners: Callable[[int, str, Dict[str, Any], Dict[str, Any], Dict[str, str]], NoneType],
    types: Optional[Set[str]] = None,
    actions: Optional[Set[str]] = None
)
```

Subscribe to new tx events. 



**Args:**
 
 - <b>`listeners`</b>:  listener functions accepting (height, txhash, tx, result, event) 


---

<a href="../../../tools/thorpy/thorpy.py#L309"><img align="right" style="float:right;" src="https://img.shields.io/badge/-source-cccccc?style=flat-square"></a>

## <kbd>function</kbd> `subscribe_msgs`

```python
subscribe_msgs(
    *listeners: Callable[[int, str, Dict[str, Any]], NoneType],
    types: Optional[Set[str]] = None
)
```

Subscribe to new transaction messages. 



**Args:**
 
 - <b>`listeners`</b>:  listener functions accepting (height, txhash, msg) 
 - <b>`types`</b>:  message types to filter 




---

_This file was automatically generated via [lazydocs](https://github.com/ml-tooling/lazydocs)._
