# 全项目维护：验证记录与手工回归清单

本轮覆盖全项目。第一阶段清理已完成；第二阶段按用户批准的 A→E 顺序，每项由独立 subagent 实现，主代理 review、复现验证并逐项提交。第三阶段的测试与文档整理单独审计、单独审批。

## 改动与提交

| 项目 | 用户可见结果 / 维护收益 | Commit |
|---|---|---|
| 第一阶段 | 删除失效 API、旧串行读取、v1 service renderer 与重复辅助逻辑；保留旧版本识别和升级保护。净减少 442 行，其中生产代码净减少 233 行。 | `27f09b1` |
| A：托管下载选项 | RPC 添加与 startup session 重建由同一策略决定 metadata、pause、verification、seeding；修复缺失 session 后 magnet 重建选项不一致。 | `27a73a5` |
| B：Dashboard 状态 | 一份列表快照、按 JobID 缓存的详情；修复快速切换任务后 Open 可能打开另一任务路径的问题，保留独立成功的详情刷新。 | `669c621` |
| C：持久状态写入 | 锁内重读并更新所属字段，runtime 提交保留最新最近目录；拒绝过期 runtime proposal，避免并发进程覆盖彼此状态。 | `c16f37b` |
| D：安装校验 | checksum 下载或校验失败时停止，拒绝缺失、格式错误、重复和不匹配的条目；保留旧二进制，不执行服务 setup。 | `f192f6d` |
| E：日志读取 | CLI 与 Doctor 共用 runtime 的有界尾部读取；消除为显示 4 KiB 而读取整份日志的开销。 | `2d7de73` |

所有提交保留在本地，没有 push、发布或替换用户当前运行的服务。

从 `5c88c8d` 到 `2d7de73`，生产代码合计净减少 208 行；测试净增加 783 行，主要用于保护此前缺少覆盖的故障与并发边界。没有为了减少行数删除关键生命周期测试，也没有新增运行时依赖。

## 验证环境与边界

- 自动化宿主：macOS arm64，Go 1.26.5，aria2c 1.37.0。
- 生产数据格式保持不变，没有 manifest、storage 或 payload 迁移。
- 自动化使用临时数据、模拟 supervisor 与本地 RPC/HTTP；真实系统服务、网络磁盘、终端和文件管理器交互仍需下面的手工验证。
- Linux 编译成功只代表编译兼容，不代表已经运行真实 systemd 服务。

## 集成验证结果

代码验证基准为 `2d7de73`，从原始 `5c88c8d` 起的六个实现提交全部包含在内。原有基线检查通过，本轮未发现新增测试失败。

| 检查 | 结果与实际范围 |
|---|---|
| `ARIA2S_LAUNCHD_INTEGRATION=0 GOFLAGS=-count=1 make test` | 全项目无缓存测试通过；真实 launchd integration 明确关闭。 |
| `go test -race -count=1 ./...` | 全项目 race 检测通过；同样关闭真实 launchd integration。 |
| `go vet ./...` | 通过。 |
| `go mod tidy -diff` / `go mod verify` | 无依赖差异，模块校验通过；没有新增运行时依赖。 |
| `gofmt -l` / `git diff --check` / `sh -n install.sh` | 无格式或 shell 语法问题。 |
| `make build MACOS_CODESIGN_IDENTITY=` | 构建成功；候选 CLI 的 version/help 可执行。 |
| `CGO_ENABLED=0` 四平台构建 | Darwin/Linux × amd64/arm64 均成功；检查了 Mach-O / ELF 产物。 |
| 原始缺陷复现 | A/B/C/E 的独立 Go overlay 复现均通过；D 的独立 shell 安装复现确认不再绕过 checksum。 |
| 真实 aria2c 集成 | loopback HTTP、BT peer/tracker、发布、暂停/恢复、magnet 重建和最终路径重启做种通过。 |

构建时 Go 尝试写工作区外的 module stat cache，被沙箱拒绝并打印非致命警告；所有构建均返回 0 且生成有效产物。现有 staticcheck 工具使用较旧 Go 构建，不适配 Go 1.26，本轮没有把它记作通过项；使用 `go vet` 完成静态检查。

## 已完成的专门验证

- A 使用真实 aria2c 1.37.0 和本地 HTTP 服务，验证了添加、完成、detach、发布后的文件字节一致。
- A 使用两个独立 aria2c 进程和 loopback tracker，完成真实 BitTorrent peer 传输，验证 descriptor promotion、发布、最终目录做种、暂停/恢复和重启后的继续做种。
- A 使用无外部 peer 的 magnet，验证 RPC 添加与缺失 session 重建后实际 native 选项一致，并保留暂停意图。这没有覆盖公共 DHT/真实 magnet metadata 交换，后者仍在手工清单中。
- 上述进程和文件都位于临时测试环境，没有修改已安装服务。测试过程发现的 macOS `/var` 与 `/private/var` 路径别名差异已按 canonical path 校验。
- B 的原始 A→B→返回 A→Open 复现已独立通过；TUI race、详情/列表部分失败、分页与缓存失效回归也通过。
- C 的原始“rebind 覆盖刚删除的最近目录”复现已独立通过；跨进程锁、取消等待、原子替换后锁 inode 保持、安装/rebind 交错更新、过期 runtime proposal 拒绝和 race 测试通过。
- D 直接执行实际 `install.sh`，使用隔离的下载器、真实 tar/哈希后端和假候选程序，覆盖 Darwin/Linux 两分支共 30 个场景。独立复现确认 checksum 下载失败返回非零且不发布二进制；失败场景不解压、不执行候选、不运行 setup。
- E 新增 1 GiB 稀疏日志的尾部正确性与上限测试。独立 64 MiB 日志复现中，输出仍为 4,106 字节，分配量从 67,151,056 字节降至 23,968 字节（单次测试观测值，不是跨环境性能保证）。
- 临时原生传输与独立审计复现没有加入默认测试依赖；对应关键行为已在仓库回归测试中覆盖，原生证明是本轮额外验收证据。

## 第三阶段只读审计

建议只执行一个文档/Help 整理任务，目前等待该阶段批准。证据与范围：

- README 的 Quick Start、命令表和 CLI Help 仍暗示 Dashboard 会安装/修复；`App.PrepareDashboard` 只复用运行服务，或验证已提交身份后启动。应明确首次安装及修复由显式 `install` 完成。
- README 的临时 HOME smoke 示例仅改变文件路径；Darwin 的 `io.github.amio.aria2s` 和 Linux 的 `aria2s.service` 身份不变，同一用户仍共享 supervisor。应改为独立测试账户/VM 的流程，避免把改 HOME 描述成服务隔离。
- managed magnet 的 metadata 选项已由 app 策略保证；README 不应继续把手工补齐旧配置中的 metadata 选项描述为托管恢复的前提。

根因是用户文档没有随服务所有权和下载策略演进同步。预计影响 README 和 `cmd/root.go` 的 Help 文案约 40–70 行；收益是消除误操作和排障歧义，风险为文案与实际行为再次偏离。验证应逐项对照当前代码、检查 CLI Help 与文档命令，不执行真实服务安装来检查文档。

没有发现值得单独实施的高收益测试删改：新测试集中在生命周期、RPC 编码、跨进程状态与安装发布边界；删掉小型低成本测试的收益不足以覆盖回归风险。`docs/reliable-managed-download-lifecycle.md` 明确保留尚待真实挂载、Linux supervisor 和 hosted CI 的外部证据，因此继续保留；不把已通过本地测试误写为外部验收完成。

## 已知边界

- C 的锁仅协调新版本写入者；旧 CLI 不参与新锁协议。状态格式保持兼容，但回退到旧版本会失去并发写入保护。
- runtime 冲突会拒绝状态提交；service artifact 准备和 state.json 发布不是一个跨文件事务。真正并发修改 runtime 的两个安装进程发生冲突后，可能需要再次执行 `install` 收敛；不会静默接受过期身份。
- checksum 服务不可用时安装明确失败，这是 D 的预期行为；当前自动化未执行真实 GitHub 下载、sudo 安装和 Homebrew 更新。
- 自动化验证无法保证真实网络、磁盘故障时序、系统 supervisor 和终端交互没有回归，必须结合以下清单验收。

## 手工验证准备

使用可丢弃的小型 HTTP 文件、一个合法可用的 magnet、一个多文件 torrent，以及含空格/中文的测试下载目录。记录当前版本和已有任务，选择允许服务短暂停止的时间。涉及缺失文件或故障注入的项目，只在测试任务或独立测试账户执行。需要完全隔离现有服务时，使用独立系统账户或 VM；只改变 HOME 不会隔离服务身份。

先构建本次工作区代码，并始终使用同一个候选二进制路径进行安装和测试。服务保存了 controller 的路径和内容身份；安装后重新编译同一路径会改变身份，需要再次安装更新身份。不要用 `aria2s update` 代替测试候选安装：它会拉取远端已发布版本。

```sh
make build
./bin/aria2s version
./bin/aria2s install --start
./bin/aria2s status
./bin/aria2s doctor
./bin/aria2s dashboard
```

下表中的 start/stop/restart/install/logs/doctor 都使用同一个 `./bin/aria2s` 候选路径，避免与 PATH 中的旧版本混用。可从日常链路的前六项开始，再完成并发状态、平台生命周期与故障边界。

## 必测：受影响的用户链路

| 完成 | 场景 | 操作与预期 |
|---|---|---|
| [ ] | TUI 快速切换后 Open | 准备目录不同的 A/B 两个任务；打开 A 的详情，再快速切到 B，B 尚未加载时按 Esc 返回列表，选 A 按 `o`。必须打开 A；随后对 B 重复。列表和详情都测。macOS 检查 Finder 的选中文件，Linux 检查文件管理器打开的目录。 |
| [ ] | 详情缓存与失败恢复 | 多次切换 A/B，停留超过 10 秒再查看；暂停/恢复后检查状态、进度和按钮立即跟随。暂时停止服务后浏览已有行，列表与详情不能串任务；服务恢复后应继续刷新，终端仍可退出。 |
| [ ] | HTTP 添加、暂停、重启与发布 | 向含空格/中文的测试目录添加文件。下载中按 `p`，重启服务，任务应继续暂停；按 `r` 后继续下载。完成后最终文件内容正确，名称和目录不变，没有多余副本。 |
| [ ] | Magnet metadata 阶段重启 | 新增 magnet，在 metadata 尚未取得时重启；应继续取得 metadata 并进入同一 JobID 的正常下载，不能多出重复托管任务。metadata 阶段暂停后重启也应保持暂停。 |
| [ ] | Torrent payload 阶段恢复 | metadata 已取得且只下载一部分时重启，继续同一任务；再测试暂停后重启。不能重新回到无意义的 metadata 获取循环，文件进度应合理恢复。 |
| [ ] | 发布后做种 | 完成一个 torrent，确认 payload 已移动到最终目录；暂停、恢复、重启后仍从最终目录做种，不重新创建旧 staging payload，不重复下载已经完成的数据。多文件 torrent 也执行一次。 |
| [ ] | 已存在同名目标 | 先在最终目录放一个同名测试文件，再完成 HTTP/torrent 下载。旧文件内容保持不变，新任务按现有规则改名；改名的 torrent 应停止，不能针对与 metainfo 不符的路径继续做种。 |
| [ ] | 最近目录与并发进程 | 同时打开两个 Dashboard，分别向不同目录添加任务；检查最近目录合并、去重、数量上限及删除。另一终端重复执行候选 `install`，再检查刚删除的目录没有复活，controller 身份没有回退，服务还能重新启动。 |
| [ ] | 平台服务生命周期 | macOS launchd 与 Linux systemd --user 分别执行 stop/start/restart，再退出并重新打开 Dashboard。状态报告应正确，任务不丢失，不产生第二个托管 aria2c 实例；配置中的自定义下载调优仍生效。 |
| [ ] | 日志与 Doctor | 服务运行时反复执行 `logs` 和 `doctor`。有日志、空日志、尚无日志的情形都应正常呈现；较大的日志文件应快速返回末尾内容，不能明显占用与日志大小相同的内存。 |

## 加测：自动化无法覆盖真实环境的高风险边界

| 完成 | 场景 | 操作与预期 |
|---|---|---|
| [ ] | 缺失 control 文件 | 仅对可丢弃测试 torrent：停止服务，备份对应文件，移走它的 `.aria2` control 文件但保留 payload 和托管 metainfo，再启动。应校验已有数据，必要时重新下载损坏部分；未验证的 staging 数据不能直接做种。不要对正在运行的真实任务删除控制文件。 |
| [ ] | 缺失 session 的 magnet 重建 | 仅在独立测试账户，停止一个尚未获取 metadata 的测试任务，备份并移走它的原生 session 记录，保留托管 manifest，再启动。应重建 metadata 任务并继续完成；结束后恢复测试环境。普通重启主要覆盖已有 session 路径，不能替代这一项。 |
| [ ] | 外置盘 / SMB | 向可移除存储下载，断开后观察错误，再连接并 Retry；验证暂停、重启及完成后的最终路径。macOS SMB 还要确认 Finder 重连授权提示和挂载路径变化后不会绑定到错误的磁盘。 |
| [ ] | Installer 网络失败 | 在独立测试账户或测试 VM，用受控网络让 archive 下载成功、checksums 下载失败。安装必须失败；原 CLI 内容/版本不变，不能继续启动安装后的服务。再放行 checksum，确认可正常安装。 |
| [ ] | 实际更新与权限 | 待候选发布后，在测试安装上运行 update，覆盖用户可写路径和需要 sudo 的路径。正在下载的任务不重启，更新后 controller 身份正确，后续服务重启成功。Homebrew 安装仍由包管理器管理。 |
| [ ] | 终端和长时间运行 | 在日常终端窗口调整尺寸、滚动多文件详情、切换分页和连续导航；运行一段时间后复查缓存刷新与 Open。真实终端绘制、操作节奏和文件管理器响应无法由模型单元测试完全替代。 |

## 回报结果

每项记录通过/失败、操作系统、aria2c 版本和候选 commit。失败时保留具体步骤、期望与实际表现，以及相关日志片段；state.json 含 RPC secret，分享前应去除该字段，避免直接贴整份文件。
