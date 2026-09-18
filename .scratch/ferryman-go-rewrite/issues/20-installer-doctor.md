# 票 20 · installer + doctor（含事件子集与 sqlite）

**What to build**：`internal/installer`：EnsureLauncher（`start "" /min "<exe>" serve >> serve.out/err.log`，CRLF）；FerryHookEntries（四事件条目结构逐字，**events 参数子集筛选**）；InstallCC(settings,db,dataDir,repo,events)（备份+幂等替换+CC Switch 地雷提示）；InjectCCSwitch（sqlite 经 modernc.org/sqlite：SELECT providers WHERE app_type='claude'、幂等注入、备份留 3）；**InstallCodex(hooksPath,configPath,repo,events)**（hooks.json + [features] hooks=true，事件子集同款——评审附录#1）；doctor 全检查（四事件齐全[**闸门缺位=提示不失败**，C12]、BOM/控制字符逐字节、ccswitch 覆盖、codex 旗标、daemon 探活、provider 在位、点火脚本有效；**HttpBeatSender 功能退化声明**输出——附录#14）。

参照：rev1 Task 24；spec §Implementation「钩子安装子集」。

**验收标准**：
- [ ] tests/test_install.py / test_ccswitch.py / test_doctor.py → 三个对应 Go 测试文件全部 1:1 移植且绿（临时 HOME/DB/配置注入）
- [ ] **子集安装用例**：events={SessionStart,SubagentStart,SubagentStop} 装完后 settings.json/hooks.json 只含这三事件
- [ ] doctor 对闸门事件缺位输出提示且不判 FAIL
- [ ] go.mod 增 modernc.org/sqlite，无 CGO

**追加验收（票 15 缓交回填）**：test_singleton.py 的 3 个 ensure_launcher 用例（测启动器产物：exe 路径/回退链/cmd 脚本内容）在 Go 侧按新形态（EnsureLauncher 生成 start "" /min "<exe>" serve 脚本）补等价测试——Python 原用例测 venv/uv 分发物，Go 无对应物故未移植，本票以新形态覆盖同语义（脚本内容/回退/幂等）。

**Blocked by**：05
