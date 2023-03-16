
Notes to self:
* many constants
* Time is a factor for calculating sum of pool slip over x blocks
* CR is dependent on the size of your loan to some extent!



# THORChain Lending System of Equations

runeBurnt = max rune supply - current rune supply <br>

totalAvailableRuneForProtocol = lever / 10_000 * runeBurnt <br> 

totalAvailableRuneForPool = (pool rune balance / total rune) * totalAvailableRuneForProtocol <br>

totalAvailableAssetForPool: convert rune value to Asset Terms. <br>

$\begin{equation}
\begin{aligned}
\texttt{getCR}(a, b, \texttt{minCR}, \texttt{maxCR}) = \frac{a}{b} \cdot (\texttt{maxCR} - \texttt{minCR}) + \texttt{minCR}
\end{aligned}
\end{equation}
$

Where: <br>
    **a:** Total Collateral for pool in Asset Terms, (total collateral + new loan collateral) <br>
    **b:** totalAvailableAssetForPool <br>
    **minCR:** minimum collateralization ratio in basis points, (LTV = 1/CR) <br> 
    **maxCR:** Maximum CR in basis points, set by Protocol. <br>

<br>
Debt = CollateralValueInTOR / CR * 10_000
<br>
another way, more intuitive: 
Debt = CollateralValueInTOR * (10_000 / CR)
<br>

## CalcAnchor
Gets values from anchor pool
* TODO

## Derived/Virtual Pool Depths

The goal of derived/virtual pools is to constrict the pool depths in periods of volatility to increase the slippage on swaps during this period of time. This incentivizes users to wait for a period of lower volatility to exit/enter the system.

### Constants/Mimirs
MaxAnchorSlip: 1500 <br>
depthBasisPts (DERIVEDDEPTHBASISPTS): 10000 <br>
minDepthPts (derivedMinDepth): 100
MaxAnchorBlocks: 300 
### Calculations
totalRuneDepth = todo

price = rune value in asset (i.e. rune per BUSD)

$\begin{equation}
\texttt{total Slip} =
\sum_{i=block}^{MaxAnchorBlocks} pool \, Slip(block)
\end{equation}
$
<br>
$
\begin{equation}
\texttt{minRuneDepth} = \frac{\texttt{minDepthsPts}}{10,000} \times \texttt{totalRuneDepth}
\end{equation}
$

minRuneDepth = (minDepthsPts/10_000) * totalRuneDepth 

<br>
<br>

$
\begin{equation}
\texttt{StartRuneDepth} = \frac{\texttt{depthBasisPts}}{10,000} \times \texttt{totalRuneDepth}
\end{equation}
$

StartRuneDepth = (depthBasisPts / 10_000) * totalRuneDepth

$
reverseSlip = slippage - maxAnchorSlip

Update rune depth to constrict based on slip:

NewRuneDepth = ((total Slip - MaxAnchorSlip) / maxAnchorSlip) * StartRuneDepth

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


Virtual Pool assetDepth = NewRuneDepth * assetValueInRune



# Loan Open

TODO

# Loan Close

TODO