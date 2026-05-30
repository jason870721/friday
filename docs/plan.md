Master Prompt for Claude (Golang Version)
[Role & Mission]
Act as a Senior Quantitative Trading Engineer and AI Architect. I currently have a basic AI Trading Agent written in Go (Golang) that can connect to exchange APIs and execute basic trading prompt strategies. However, its decision-making is currently too short-sighted and easily disrupted by market noise.

Your task is to help me refactor and upgrade this Agent's Go codebase step-by-step according to the 4 Phases outlined below.

IMPORTANT: We will work iteratively. Please focus ONLY on one phase at a time. Do not write the code for all phases at once. Let's start with Phase 1 first.

[Phase 1: Data Semanticization & ReAct Pattern (Quick Wins)]

Objective: Enable the LLM to understand market data contextually and force it to reason before acting.

Tasks:

Data Preprocessing: Modify the historical price arrays (OHLCV) currently fed to the Agent. Add Go functions/methods to calculate basic technical indicators (e.g., MA20, RSI) and format them into "natural language strings" (e.g., "The current price has broken above MA20, and RSI is at 72").

ReAct Framework: Update the Agent's System Prompt. Enforce a rule that before outputting any API execution command, it must first output a <Thought> block to analyze the current market trend and explain why it aligns with the strategy.

[Phase 2: External Sentiment & Hard-coded Risk Guardrails]

Objective: Expand decision dimensions and prevent catastrophic trades.

Tasks:

Tool Integration: Add new callable tools (Go functions) for the Agent to fetch the "Fear & Greed Index" or real-time financial news APIs.

Pre-trade Guardrail: Insert a hard-coded Go risk-control logic block (e.g., a validator struct or middleware interface) right after the Agent generates a "trade action" but before the actual API execution. This logic should check if the proposed order size exceeds a safe threshold (e.g., 5% of total balance). If it does, block the trade and prompt the Agent to recalculate.

[Phase 3: Multi-Agent Architecture Refactoring]

Objective: Solve the context overload issue of a single Agent by leveraging Go's concurrency model.

Tasks: Break down the monolithic Agent into three collaborative entities (implemented via Go structs, interfaces, and potentially Goroutines/Channels for communication):

Analyst Agent: Uses data and tools from Phases 1 & 2 to generate a market analysis report (bullish/bearish context).

Risk Manager Agent: Reads the analyst's report, calculates the appropriate position size, sets stop-loss/take-profit levels, or vetoes high-risk proposals.

Executor Agent: Receives strict numerical parameters from the Risk Manager and executes the precise API calls to the exchange.

[Phase 4: Vector Memory & Sandbox Backtesting (Advanced)]

Objective: Equip the system with self-reflection and hypothesis validation capabilities.

Tasks:

Trade Log Memory: Integrate a lightweight vector database using a Go SDK (e.g., ChromaDB, Milvus, or Qdrant). After closing a trade, log the "market context, entry reason, and PnL result." Before evaluating new trades, the Analyst Agent should retrieve past logs of similar market conditions as reference.

Sandbox Tool: Implement a RunBacktest(strategyLogic string) method. Allow the Agent to call this tool to simulate win rates on historical data before executing newly formulated logic in the live market.

[Initialization]
Please acknowledge this roadmap. Let's begin with Phase 1. Tell me what parts of my current Go codebase (e.g., main structs, API client, prompt templates) you need to see first to get started on Phase 1.