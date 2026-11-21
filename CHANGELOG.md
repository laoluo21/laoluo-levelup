# 更新记录

## v0.3.5 · 用现有 Mengka 域名打开管理页

本次更新解决了托管 Docker 插件的管理网页只监听在容器内，宿主机 Nginx 无法连接的问题。现在可通过 Unix Socket 把管理页接到 `https://你的-Mengka-域名/laoluo-levelup/`，沿用现有 HTTPS 证书，无需映射 `18890`。

- 安装包附带 `scripts/setup-admin-nginx.py`，适用于宿主机运行 Nginx 的 Linux Docker + systemd 部署。工具只挂载 Socket 子目录，并以 Nginx 工作用户验证上游连通性。
- 导入并启动插件后运行接入工具，将它输出的路径配置加入现有 HTTPS 站点。保留原插件数据卷的更新可以沿用入口与管理密码。 [完整操作说明](管理页面访问说明.md)
- 同时合入 3.2.3 兼容调整。移除部分框架导入校验拒绝的 `get_plugin_context` 声明及启动调用，按扫码接口返回的 `need_a1` 处理免扫授权，并跳过今天已签到但没有加速奖励的会员任务。

管理入口已在 Mengka 托管 Docker 部署上通过公网 HTTPS 验证。独立容器测试覆盖首次设置密码、配置保存及重建容器后的数据保留。真实好友 QQ 操作仍待完整联调。
