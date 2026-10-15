# 公开集成与纯应用组合

[English](README.md) · [能力地图](../core/README.zh.md)

| 包 | 调用方与职责 |
| --- | --- |
| [profile](profile) | 插件元数据、有效 Profile codec、Bundle 定义/codec、脱离原件的 Clone，以及只读 BundleCatalog 接口 |
| [registration](registration) | PluginRegistration 将 Gordis 原型与 Profile、调用元数据绑定 |
| [launch](launch) | 有限消费者使用 Parser/Invocation/Prepared、Context、ProcessControl、Task 和 HeaderCheckBinder |
| [boot.Run](run.go) | 应用进程入口，启动显式 Profile，拥有信号处理与清理 |
| [application](application) | 工厂与组装方声明 Extension/Application；Compose 校验静态元数据并冻结显式 Bundle 定义 |

Provider 与贡献生命周期通过 [core/gordisbridge](../core/gordisbridge) 接入；领域定义归 [core](../core/README.zh.md)。元数据声明和 Compose 不激活插件或创建 Host；Compose 不隐式选择 builtin。

`boot.Run` 是进程入口。私有 CLI、严格用户源 codec/预检、Bundle 目录、Coordinator、Host runtime 与进程监督归本目录的 `internal`。Compose 只依赖纯检查与目录构造，私有执行只使用公开叶子契约，不反向导入 application 或 boot。使用 boot.Run 和各公开 owner；私有构造器留在 boot/internal。

下面是一个完整的内建应用入口。`builtin` 是 `builtin/bundle` 包的导入别名；自己的扩展可作为额外参数传给 Compose。用户配置规则见 [Profile 参考](../docs/profiles.zh.md)。

```go
package main

import (
	"fmt"
	"os"

	builtin "github.com/RussellLuo/qi/builtin/bundle"
	"github.com/RussellLuo/qi/kernel/boot"
	"github.com/RussellLuo/qi/kernel/boot/application"
	"github.com/RussellLuo/qi/kernel/boot/launch"
)

func main() {
	app, err := application.Compose(builtin.Extension())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(boot.Run(os.Args[1:], launch.Input{
		Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
	}, app))
}
```

Extension 字段直接使用 `registration.PluginRegistration` 和 `profile.BundleDefinition`，Application 只暴露 `profile.BundleCatalog.Lookup`。具体目录构造和启动控制属于私有实现。每个类型只有一个公开定义。

纯 Compose、严格 Bundle 测试归本目录；用户源/预检和 Coordinator 用例随私有 owner；boot 的进程 fixture 与真正外部 module 由本地 Go 测试明确构建运行。构建器自己拥有外部 fixture，由 `internal/build/build_test.go` 实际生成、编译并执行。
