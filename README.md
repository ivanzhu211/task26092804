# task26092804
数字菜单屏看板物化缓存库（纯内存，Go）。

## 用法

```go
c := menuboard.New(hook) // hook 可为 nil，每次真正渲染时回调 (displayID, version)

v1, _ := c.DefineBoard("screen-1", menuboard.BoardDef{
    Sections: [][]string{{"item-1", "item-2"}}, // 最多 6 个分区，每个最多 40 个 ID
})
v2, _ := c.ApplyBatch([]menuboard.ItemChange{
    {Kind: menuboard.Upsert, ID: "item-1",
        Name: menuboard.Provide("汉堡"), BasePrice: menuboard.Provide(int64(2500))},
    {Kind: menuboard.Delete, ID: "item-2"},
}) // 最多 200 条，原子生效，任一不合法整批拒绝且不占用版本号
board, version, _ := c.GetBoard("screen-1")
```

- 价格一律为整数分；套餐价/加大价为 `*int64`，可空。
- upsert 只改 `Provide(...)` 给出的字段；`MealPrice`/`LargePrice` 用 `Provide((*int64)(nil))` 清除。
- `GetBoard` 命中缓存时不获取任何共享锁；返回的 `Board` 是拷贝，可自由修改。

## 测试

```sh
go test -race ./...
```
