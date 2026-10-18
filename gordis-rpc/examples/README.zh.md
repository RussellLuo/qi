# 示例

[English](README.md) | 中文

从 [basic](basic/README.zh.md) 开始：普通 Greeter Plugin、生成的 typed Binding、库内通用
process 加载器，以及独立插件程序 Unit。

从 `gordis-rpc/` 运行 `GOWORK=off go test -race -count=1 ./examples/...`，验证真实
Host 和插件程序、配置、Local/process 对照和依赖边界。

duplex、stream、assembly、events 属于后续阶段，目前尚未实现。
