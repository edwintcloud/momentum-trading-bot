# Project Memory – Momentum Trading System

This document captures the full context of the trading bot project so AI assistants can maintain continuity.

---

# Project Goal

Build a fully automated algorithmic trading system that trades US stocks using momentum strategies.

The system should:

- scan the entire stock market
- detect momentum breakouts
- enter and exit trades automatically
- manage risk
- provide a real-time dashboard

---

# Strategy Overview

The strategy is based on **momentum trading**.

Specifically:

Low-float momentum breakout trading.

These strategies attempt to capture large intraday moves in small-cap stocks.

The system is not trying to predict 100–1000% moves before they start.

Instead it attempts to:

detect momentum early  
enter during breakout  
capture 10–40% segments

---

# Typical Characteristics of Target Stocks

Target stocks often have:

- small float
- high volatility
- news catalyst
- unusual volume

These characteristics allow large price moves.

---

# Screening Rules

The scanner identifies candidate stocks using these filters.

Base filters:

price > $1  
gap > 10%  
relative volume > 5  
float < 50M  
premarket volume > 500k

These filters reduce thousands of stocks to a small set of candidates.

---

# Entry Strategy

Entry occurs on **breakout confirmation**.

Typical trigger:

price breaks high-of-day  
volume spike occurs

This indicates strong buying momentum.

---

# Exit Strategy

The system manages risk automatically.

Typical rules:

stop loss: -5%  
profit target: 10–20%  
runner: trailing stop

The goal is to capture quick momentum moves.

---

# Risk Management

Risk control is critical.

Rules include:

max trades per day  
risk per trade = 1% capital  
daily loss limit  
emergency stop

The system must prevent catastrophic losses.

---

# Architecture Overview

The system is written in Go.

Major modules:

market data  
scanner  
strategy  
risk engine  
execution engine  
portfolio tracker  
web API

The dashboard is written in React.

---

# Data Flow

Typical trading pipeline:

market data stream → scanner → strategy → risk check → execution → portfolio update → dashboard

Signals move through the system via channels.

---

# Concurrency Model

Go concurrency is used to scan many stocks simultaneously.

Typical design:

one goroutine per symbol scan  
worker pools for processing  
channels for signal communication

This allows the system to scale to thousands of symbols.

---

# Execution

Orders are sent to the Alpaca brokerage API.

Initially the system should run in:

paper trading mode

before any live trading occurs.

---

# Dashboard

A React dashboard will provide visibility and control.

The dashboard should show:

system status  
scanner candidates  
open positions  
PnL  
logs

Controls should include:

pause algorithm  
resume algorithm  
close all positions  
emergency stop

---

# Safety Features

Critical safety mechanisms include:

kill switch  
daily loss limit  
max exposure control

The dashboard must be able to pause trading instantly.

---

# Long-Term Vision

Eventually the system may expand to include:

advanced scanners  
short squeeze detection  
machine learning signal ranking  
news sentiment analysis  
backtesting engine  
performance analytics

---

# Important Development Principle

Keep the system:

simple  
modular  
safe  
observable

Reliability is more important than complexity.

---

# End of Project Memory