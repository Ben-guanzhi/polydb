# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Multi-engine database client (SQLite, MySQL, PostgreSQL, SQL Server, Oracle, Redis)
- Rust workspace with 16 crates
- Go implementation with HTTP server
- React + Monaco web frontend
- SSH tunnel support with known_hosts TOFU verification
- AES-256-GCM encrypted keyring (file backend + OS keyring)
- Large BLOB sidecar storage (spec/encoding.md section 5.2)
- Contract tests with 5 real database engines
- Docker deployment
- GPUI desktop GUI

### Changed
- Dialect abstraction: DriverSpec registry pattern for Rust and Go
- DialectCapabilities struct for Web ALTER SQL generation
- Quoter/Escaper interfaces for sqltui backend pattern
- Dialect struct with function-based registry for tableprosql

## [0.1.0] - 2026-10-13

### Added
- Initial release
- Dual implementation (Rust + Go) sharing language-neutral spec/
- MessagePack encoding for data plane
- OpenAPI + AsyncAPI specification
- Schema diff engine (DDL generation + comparison)
- Redis KV driver support
- SSH tunnel integration
- Cross-language contract tests
- Protocol code generation from spec/
