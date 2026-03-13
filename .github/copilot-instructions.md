# Copilot Instructions – Momentum Trading Bot

## Project Overview

This repository contains a fully automated algorithmic trading system written in Go.

The system trades US equities using the Alpaca brokerage API and implements a **low-float momentum breakout strategy**.

The trading engine:

- scans the entire US stock market continuously
- detects high-momentum stocks
- enters trades automatically
- manages risk and exits positions
- streams live status to a React dashboard

The goal is to build a **production-grade algorithmic trading system**.

---

# Technology Stack

Backend:

- Go (primary language)
- Alpaca trading API
- WebSockets for real-time updates
- PostgreSQL for persistent data
- Redis for caching (future)

Frontend:

- React dashboard
- WebSocket live updates

Deployment (future):

- Docker containers
- Cloud deployment

---

# Coding Standards

## Go Conventions

Follow standard Go conventions:

- idiomatic Go
- small focused packages
- concurrency via goroutines and channels
- avoid global mutable state

Use clear package boundaries.

---

# Core System Components

The system is divided into the following modules:

## 1 Market Data Engine

Responsible for:

- streaming stock data
- computing indicators
- feeding the scanner

Files typically live in: internal/market/
Responsibilities:

- maintain real-time price state
- compute relative volume
- compute gap %
- compute high-of-day

---

## 2 Scanner Engine

Scans the entire stock universe for candidates.

Location: internal/scanner/
Responsibilities:

- scan all stocks concurrently
- detect abnormal activity
- emit signals

The scanner should support scanning **thousands of symbols concurrently** using goroutines.

---

## 3 Strategy Engine

Implements the trading strategy.

Location: internal/strategy/
Current strategy:

Low-float momentum breakout.

Stock candidate conditions:

- price > $1
- gap > 10%
- relative volume > 5
- float < 50M

Entry trigger:

- break of high-of-day
- strong volume spike

---

## 4 Risk Engine

Location: internal/risk/
Responsibilities:

- enforce max trades per day
- enforce position sizing
- enforce daily loss limits
- prevent runaway trading

Risk rules must always be checked before execution.

---

## 5 Execution Engine

Location: internal/execution/
Responsible for:

- placing orders
- managing positions
- interfacing with Alpaca API

Orders should support:

- market orders
- limit orders (future)
- stop loss
- partial profit

---

## 6 Portfolio Tracker

Location: internal/portfolio/
Tracks:

- open positions
- PnL
- trade history

---

## 7 Web API

Location: internal/api/
Provides:

- WebSocket streaming
- REST endpoints
- control interface

Endpoints include:
/api/status
/api/pause
/api/resume
/api/positions
/api/close-all
---

# Concurrency Design

The system must be highly concurrent.

Patterns to use:

- goroutines
- worker pools
- channels

Example flow:

scanner → strategy → risk → execution

Signals should be passed via channels.

---

# Safety Requirements

This is a trading system. Safety is critical.

Always implement:

- kill switch
- pause/resume control
- daily loss limits
- position limits

The dashboard must be able to stop trading instantly.

---

# Logging

All major events must be logged:

- trades
- signals
- errors
- risk blocks

Logs should be structured.

---

# Testing

Prefer:

- unit tests for strategy logic
- unit tests for risk engine
- mocked Alpaca execution

Avoid live trading in tests.

---

# Performance Goals

The scanner should be able to:

- monitor 8000+ stocks
- process updates every few seconds
- generate signals in real time

Use efficient data structures and concurrency.

---

# AI Coding Guidance

When generating code:

1. Keep packages modular.
2. Avoid unnecessary complexity.
3. Prefer simple and robust designs.
4. Ensure risk checks occur before execution.
5. Avoid blocking operations in scanner loops.

---

# Future Features

Potential future upgrades include:

- VWAP breakout detection
- float rotation detection
- short squeeze detection
- news catalyst detection
- machine learning ranking
- backtesting engine
- performance analytics

These should be designed as modular extensions.

---

# Summary

This project is a **production-grade automated trading system**.

Focus on:

- reliability
- safety
- concurrency
- clear architecture