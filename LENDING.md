# THORChain Lending System of Equations

[lending Design Issue !1412](https://gitlab.com/thorchain/thornode/-/issues/1412)

---
`LendingLever`: throttles amount of rune available for lending, basis points.


$\text{runeBurnt} = \text{rune supply} - \text{current rune supply} $

[runeBurnt link](https://gitlab.com/thorchain/thornode/-/blob/develop/x/thorchain/handler_loan_open.go#L203) 

<br>

$
\text{totalAvailableRuneForProtocol} = \frac{\text{LendingLever}}{10,000} \times \text{runeBurnt}
$

<br>

$ 
\text{totalAvailableRuneForPool} = \frac{\text{pool rune balance}}{\text{total rune}} \times \text{totalAvailableRuneForProtocol}
$

<br>

`totalAvailableAssetForPool`: convert rune value to Asset Terms. <br>

## [Collateralization Ratio](https://gitlab.com/thorchain/thornode/-/blob/develop/x/thorchain/handler_loan_open.go#L305)

$\begin{equation}
\begin{aligned}
\texttt{getCR}(a, b, \texttt{minCR}, \texttt{maxCR}) = \frac{a}{b} \cdot (\texttt{maxCR} - \texttt{minCR}) + \texttt{minCR}
\end{aligned}
\end{equation}
$

Where: <br>
- **a:** Total Collateral for pool in Asset Terms, (total collateral + new loan collateral) <br>
- **b:** totalAvailableAssetForPool <br>
- **minCR:** minimum collateralization ratio in basis points, (LTV = 1/CR) <br>
- **maxCR:** Maximum CR in basis points, set by Protocol. <br>

## [Debt](https://gitlab.com/thorchain/thornode/-/blob/develop/x/thorchain/handler_loan_open.go#L197)

$$
\begin{equation}
\texttt{Debt} = \frac{\texttt{CollateralValueInTOR}}{\texttt{CR}} \times 10,000
\end{equation}
$$

more intuitive:

$$
\begin{equation}
\texttt{Debt} = \texttt{CollateralValueInTOR} \times \frac{10,000}{\texttt{CR}}
\end{equation}
$$

Where:

- **Debt**: represents obligation of borrower in TOR terms.
- **CollateralValueInTOR**: collateral value in TOR.
- **CR**: collateralization ratio.

<br>


## Derived/Virtual Pool Depths

Derived/virtual pool depths expand and contract to throttle the amount of slippage users pay. In periods of volatility, pool depth contracts to increase the slippage on swaps during this period of time. This incentivizes users to wait for a period of lower volatility to exit/enter the system.

<br>

### Constants/Mimirs
[constants link](https://thornode.ninerealms.com/thorchain/constants), [Mimir link](https://thornode.ninerealms.com/thorchain/mimir)

`MaxAnchorSlip`: 1500 <br>
`depthBasisPts` (DERIVEDDEPTHBASISPTS): 10000 <br>
`minDepthPts` (derivedMinDepth): 100
`MaxAnchorBlocks`: 300

### Calculations

`totalRuneDepth`: Sum of all rune in pools

<br>

$\begin{equation}
\texttt{total Slip} =
\sum_{i=block}^{MaxAnchorBlocks} pool \, Slip(i)
\end{equation}
$
<br>
$
\begin{equation}
\texttt{minRuneDepth} = \frac{\texttt{minDepthsPts}}{10,000} \times \texttt{totalRuneDepth}
\end{equation}
$

<br>


$
\begin{equation}
\texttt{StartRuneDepth} = \frac{\texttt{depthBasisPts}}{10,000} \times \texttt{totalRuneDepth}
\end{equation}
$

Update rune depth to constrict based on slip:

$
\begin{equation}
\texttt{NewRuneDepth} = \frac{\texttt{totalSlip} - \texttt{MaxAnchorSlip}}{\texttt{maxAnchorSlip}} \times \texttt{StartRuneDepth}
\end{equation}
$

$
\begin{equation}
\texttt{VirtualPoolAssetDepth} = \texttt{NewRuneDepth} \times \texttt{assetValueInRune}
\end{equation}
$


### Rune Burn / Mint
- TODO


### CalcAnchor

Gets values from anchor pool

- TODO

# Loan Open

Steps:
1. User sends in collateral
2. add user collateral to existing collateral for pool
3. Calculate Collateralization Ratio, getcr()
4. calculate debt (in TOR)
5. calculate virtual pool depth
6. swap (TOR) to desired asset
7. user recives debt in desired asset

# Loan Close

Steps:
1. User sends in payment
2. payment is swapped to TOR
3. - TODO