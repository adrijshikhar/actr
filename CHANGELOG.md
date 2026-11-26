# Changelog

## [0.4.0](https://github.com/adrijshikhar/actr/compare/actr-v0.3.0...actr-v0.4.0) (2026-11-26)


### Features

* **action:** implement action resolver with advisory lock and zip slip protection ([f094db6](https://github.com/adrijshikhar/actr/commit/f094db67d5e80baf04c9accca084afdc102ce0c8))
* **action:** implement action.yml manifest parser ([362ed01](https://github.com/adrijshikhar/actr/commit/362ed015f442f12393a4668ad0a590a611678dd7))
* **action:** implement host node runtime detector ([8ec35f3](https://github.com/adrijshikhar/actr/commit/8ec35f32292fed3ecf0ded066aef8f312011c569))
* **action:** implement step execution adapter and dual-key environment builder ([9e47896](https://github.com/adrijshikhar/actr/commit/9e47896ddcde92ea41bbc62ec2a0845a91877512))
* **cli:** Native Shell Autocompletion & Completion Installer ([#11](https://github.com/adrijshikhar/actr/issues/11)) ([01b5826](https://github.com/adrijshikhar/actr/commit/01b58261203fa3044debc2cc22bc4646217993c2))
* **cli:** wire multi-tenant supervisor into actr run, register, and service commands ([bcf2326](https://github.com/adrijshikhar/actr/commit/bcf232662b82f5ab716a1e02e9f5aca8e8a570c8))
* **cli:** wire sqlite history store and purge legacy demo runs ([deb6281](https://github.com/adrijshikhar/actr/commit/deb62817f2f97e965cfb31b9d8367e2e775d87d2))
* **config:** add multi-tenant supervisor schema and loader ([f434082](https://github.com/adrijshikhar/actr/commit/f434082349da7689df817e3a15fafb4b9d920788))
* **demo:** update demo runner to multi-repo supervisor and slot contracts ([#8](https://github.com/adrijshikhar/actr/issues/8)) ([8fdc02e](https://github.com/adrijshikhar/actr/commit/8fdc02ed60edc54c6537b73657e6f6d9f5c9505e))
* embedded web dashboard, real-time log streaming, and telemetry charts (Milestone 7) ([#13](https://github.com/adrijshikhar/actr/issues/13)) ([c44df7e](https://github.com/adrijshikhar/actr/commit/c44df7eec6a7fc804095656b5193789310d55dd9))
* **engine:** expose RUNNER_TOOL_CACHE and standard runner environment ([c257379](https://github.com/adrijshikhar/actr/commit/c257379a2951927c1ea4cd00a5ba8200007f7a6a))
* **engine:** harden action lifecycle, symlink traversal, process cleanup, and expression inputs ([c95f6e9](https://github.com/adrijshikhar/actr/commit/c95f6e952864926d8a19a4726536f24b4ba23afe))
* **engine:** implement dynamic toolchain ingestion from GITHUB_PATH and GITHUB_ENV ([813b136](https://github.com/adrijshikhar/actr/commit/813b1364574bff1d01a30f0a12661c8c02c1c47d))
* **engine:** implement LIFO post-step hooks with decoupled context ([ad79447](https://github.com/adrijshikhar/actr/commit/ad79447349330bf644e76d6fc40bcb0ad1f2e171))
* **history:** implement pure-Go SQLite storage engine ([15dcd87](https://github.com/adrijshikhar/actr/commit/15dcd8719a315d5e9a919b1ec561c57a6b260b45))
* initial release v0.1.0 ([c97fd57](https://github.com/adrijshikhar/actr/commit/c97fd575764b5b0e6c95fff45c549e3d80a0df07))
* **monitor:** add RepoAlias, SupervisorStatus, and strict per-job telemetry attribution ([ec682a3](https://github.com/adrijshikhar/actr/commit/ec682a39bc677249ff7fa3b6c82e693cdfce8e69))
* **otel:** enrich telemetry with k8s metrics, w3c trace propagation, and bottom system vitals ([#9](https://github.com/adrijshikhar/actr/issues/9)) ([6ea5764](https://github.com/adrijshikhar/actr/commit/6ea5764f7a8a25ceb7fb0d3416fd6cd2a21ea878))
* **protocol:** preserve action reference in JobStep ([526daa0](https://github.com/adrijshikhar/actr/commit/526daa002e2f255fbc672d7fb6d199f39563f44a))
* **supervisor:** implement core Supervisor lifecycle, worker management, and dynamic reload ([54091c4](https://github.com/adrijshikhar/actr/commit/54091c40cdbe02f1fcf2eddd2892d1b36b359a57))
* **supervisor:** implement RepoWorker with capacity-gated polling and RAII permit guard ([a3e1b3d](https://github.com/adrijshikhar/actr/commit/a3e1b3df5ce4ee3654f3ce1c058910e362b8e96e))
* **supervisor:** implement ResourceManager with counting semaphore and capacity notification ([72ca834](https://github.com/adrijshikhar/actr/commit/72ca83446a70dc0021802fdd1bc046553d384c41))
* **test:** add end-to-end multi-tenant execution, capacity limits, and IPC reload test suite ([912d48e](https://github.com/adrijshikhar/actr/commit/912d48e2a61b65242ccab3359845adc2d5ea4f43))
* **tui:** implement hierarchical multi-repo tree grouping, HUD slot vitals, and expansion preservation ([47397e2](https://github.com/adrijshikhar/actr/commit/47397e255817c666b41bccab47f8828211685570))
* **tui:** implement interactive sidebar tree search filtering ([c6387f8](https://github.com/adrijshikhar/actr/commit/c6387f8955f962a15bcc2a061cfae9fb4dc71058))
* **tui:** in-log content search and match navigation ([23497d8](https://github.com/adrijshikhar/actr/commit/23497d8eb1404e7d2809a479fb3c075c9f3c3c64))
* **tui:** mouse tree selection, drag highlight, and clipboard copy ([2958b4d](https://github.com/adrijshikhar/actr/commit/2958b4de808d4e9b7e7bf4b733627c816decab3d))
* **tui:** permanent linked repos in live runs and repo-grouped history ([d564e5e](https://github.com/adrijshikhar/actr/commit/d564e5edfaf479667fa3c9a95e42f9b9c698e0aa))
* **vault:** Runner Secret Vault & Pluggable Providers (Milestone 6) ([#10](https://github.com/adrijshikhar/actr/issues/10)) ([abe2bba](https://github.com/adrijshikhar/actr/commit/abe2bba2274d8ce41164a1d3b2038d30a2612b86))


### Bug Fixes

* **action:** ensure resolved action paths are absolute ([aa7fa7a](https://github.com/adrijshikhar/actr/commit/aa7fa7a21c3300275153f727c54d9b495d0ba2f7))
* address openqodex code review findings across sqlite store, tui, and cli ([1c7b7ef](https://github.com/adrijshikhar/actr/commit/1c7b7ef967251ce7a11bc1183465447cec094d56))
* **ci:** trust tap before audit and explicitly authenticate push in Homebrew workflow ([#12](https://github.com/adrijshikhar/actr/issues/12)) ([acaf77f](https://github.com/adrijshikhar/actr/commit/acaf77ff047f52594ebbdd2a011bd86450513e80))
* **cli:** address review findings for multi-repo history persistence, limits validation, and sighup lifecycle ([74b7d70](https://github.com/adrijshikhar/actr/commit/74b7d70604cb8bea612b31cfc601519f48905cb1))
* **core:** bug bounty hardening across pubsub, keychain, sqlite, and execution engine ([#15](https://github.com/adrijshikhar/actr/issues/15)) ([a86632d](https://github.com/adrijshikhar/actr/commit/a86632dcd299adebe1c75ae8ee4114b213748bbb))
* **core:** execution, concurrency, and security hardening (PR 1) ([#16](https://github.com/adrijshikhar/actr/issues/16)) ([95a2efb](https://github.com/adrijshikhar/actr/commit/95a2efb3380ff8ce4246e4343301d5eff5688bc4))
* **core:** protocol, supervisor, vault, and storage hardening (PR 2) ([#17](https://github.com/adrijshikhar/actr/issues/17)) ([ef7c5c4](https://github.com/adrijshikhar/actr/commit/ef7c5c4b3be07711bf47af65c018fb7128e537c0))
* **core:** web dashboard, cli locks, service escaping, tui stream cleanup, and strict lint (PR 3) ([#19](https://github.com/adrijshikhar/actr/issues/19)) ([3c51bad](https://github.com/adrijshikhar/actr/commit/3c51bada28a64c6f7601e4a8a5c5b251749bd46f))
* **engine:** address multi-agent review findings ([91f8a26](https://github.com/adrijshikhar/actr/commit/91f8a2666f39f0b069ef379b3e493e91517d0347))
* **history:** parameterize limit query and establish proper io.Closer and Clipboard interfaces ([c20d0de](https://github.com/adrijshikhar/actr/commit/c20d0de8aca029848007e3d75e8dfaac4aa0fb23))
* **monitor:** add defensive copy in SetSupervisorStatus and extend reload and telemetry tests ([fa70d2a](https://github.com/adrijshikhar/actr/commit/fa70d2a0a3a6439e75a5ecf53c7412343666bfe1))
* post-merge hardening for Windows browser launch, checkout authentication, secret masking, and TUI hydration ([#2](https://github.com/adrijshikhar/actr/issues/2)) ([#2](https://github.com/adrijshikhar/actr/issues/2)) ([12fb2fc](https://github.com/adrijshikhar/actr/commit/12fb2fcdae11514a17047c259aea2cfd752bf3a3))
* **runner:** support step conditions, artifact vss endpoints, and job env inheritance ([#6](https://github.com/adrijshikhar/actr/issues/6)) ([b0225fd](https://github.com/adrijshikhar/actr/commit/b0225fdd40f97c4e3855beb1255e814e72a756f2))
* **supervisor:** cancel polling on Stop, drain jobs before session delete, and establish broker session ([ecc0db5](https://github.com/adrijshikhar/actr/commit/ecc0db5c31b7b89165c7d139679bd2af219ab7c3))
* **supervisor:** ensure relative workFolder is scoped to repo ConfigDir ([827272d](https://github.com/adrijshikhar/actr/commit/827272d6f09dd25a05d91f7b1ec680dbca99be11))
* **supervisor:** make reload atomic under lock and prevent reload during shutdown ([15bdf87](https://github.com/adrijshikhar/actr/commit/15bdf875a160905ac57b5fc6548b846b43d95e73))
* **supervisor:** track executing jobs via atomic counter to avoid false busy status during long-poll ([b9751fe](https://github.com/adrijshikhar/actr/commit/b9751fedd426a3cf4c777c36a223a679aff3dda7))
* **tui:** set explicit NodeType on dynamic jobs/steps and add active HUD slots test ([4192f4c](https://github.com/adrijshikhar/actr/commit/4192f4c849882af8bfd443392173efd21e58fde5))
* **tui:** stabilize sidebar height and improve click responsiveness ([#7](https://github.com/adrijshikhar/actr/issues/7)) ([a4c4b8a](https://github.com/adrijshikhar/actr/commit/a4c4b8a4dc020825c61c59bf346ea6dd7cdfa260))
* v0.1.1 reliability, concurrency, security and supply chain hardening ([#1](https://github.com/adrijshikhar/actr/issues/1)) ([8dde59e](https://github.com/adrijshikhar/actr/commit/8dde59e253c80a68c53afb6496d396c1596a1086))
* **web:** live logs, metrics, and daemon status on web dashboard ([#14](https://github.com/adrijshikhar/actr/issues/14)) ([a5e7b3c](https://github.com/adrijshikhar/actr/commit/a5e7b3cacd9490e14dfcb2caca46cb6bcf63c5fc))

## [0.3.0](https://github.com/adrijshikhar/actr/compare/v0.2.0...v0.3.0) (2026-11-26)


### Features

* **cli:** Native Shell Autocompletion & Completion Installer ([#11](https://github.com/adrijshikhar/actr/issues/11)) ([01b5826](https://github.com/adrijshikhar/actr/commit/01b58261203fa3044debc2cc22bc4646217993c2))
* **demo:** update demo runner to multi-repo supervisor and slot contracts ([#8](https://github.com/adrijshikhar/actr/issues/8)) ([8fdc02e](https://github.com/adrijshikhar/actr/commit/8fdc02ed60edc54c6537b73657e6f6d9f5c9505e))
* embedded web dashboard, real-time log streaming, and telemetry charts (Milestone 7) ([#13](https://github.com/adrijshikhar/actr/issues/13)) ([c44df7e](https://github.com/adrijshikhar/actr/commit/c44df7eec6a7fc804095656b5193789310d55dd9))
* **otel:** enrich telemetry with k8s metrics, w3c trace propagation, and bottom system vitals ([#9](https://github.com/adrijshikhar/actr/issues/9)) ([6ea5764](https://github.com/adrijshikhar/actr/commit/6ea5764f7a8a25ceb7fb0d3416fd6cd2a21ea878))
* **vault:** Runner Secret Vault & Pluggable Providers (Milestone 6) ([#10](https://github.com/adrijshikhar/actr/issues/10)) ([abe2bba](https://github.com/adrijshikhar/actr/commit/abe2bba2274d8ce41164a1d3b2038d30a2612b86))


### Bug Fixes

* **ci:** trust tap before audit and explicitly authenticate push in Homebrew workflow ([#12](https://github.com/adrijshikhar/actr/issues/12)) ([acaf77f](https://github.com/adrijshikhar/actr/commit/acaf77ff047f52594ebbdd2a011bd86450513e80))
* **core:** bug bounty hardening across pubsub, keychain, sqlite, and execution engine ([#15](https://github.com/adrijshikhar/actr/issues/15)) ([a86632d](https://github.com/adrijshikhar/actr/commit/a86632dcd299adebe1c75ae8ee4114b213748bbb))
* **core:** execution, concurrency, and security hardening (PR 1) ([#16](https://github.com/adrijshikhar/actr/issues/16)) ([95a2efb](https://github.com/adrijshikhar/actr/commit/95a2efb3380ff8ce4246e4343301d5eff5688bc4))
* **core:** protocol, supervisor, vault, and storage hardening (PR 2) ([#17](https://github.com/adrijshikhar/actr/issues/17)) ([ef7c5c4](https://github.com/adrijshikhar/actr/commit/ef7c5c4b3be07711bf47af65c018fb7128e537c0))
* **core:** web dashboard, cli locks, service escaping, tui stream cleanup, and strict lint (PR 3) ([#19](https://github.com/adrijshikhar/actr/issues/19)) ([3c51bad](https://github.com/adrijshikhar/actr/commit/3c51bada28a64c6f7601e4a8a5c5b251749bd46f))
* **runner:** support step conditions, artifact vss endpoints, and job env inheritance ([#6](https://github.com/adrijshikhar/actr/issues/6)) ([b0225fd](https://github.com/adrijshikhar/actr/commit/b0225fdd40f97c4e3855beb1255e814e72a756f2))
* **tui:** stabilize sidebar height and improve click responsiveness ([#7](https://github.com/adrijshikhar/actr/issues/7)) ([a4c4b8a](https://github.com/adrijshikhar/actr/commit/a4c4b8a4dc020825c61c59bf346ea6dd7cdfa260))
* **web:** live logs, metrics, and daemon status on web dashboard ([#14](https://github.com/adrijshikhar/actr/issues/14)) ([a5e7b3c](https://github.com/adrijshikhar/actr/commit/a5e7b3cacd9490e14dfcb2caca46cb6bcf63c5fc))
