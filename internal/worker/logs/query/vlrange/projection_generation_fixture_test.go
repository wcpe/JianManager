package vlrange

// projectionGenerationField 是历史事件里仍在携带的投影代次字段名。
//
// 2026-11-02 简化投递路径后查询侧**不再**渲染或消费该字段：
// selectorQuery 不生成 `projection_generation` 谓词，mergedSearchQuery 的 fields
// 列表也不再取它，rangeMatchesEvent 不做代次过滤（第四段已删）。
//
// 但历史数据上仍然带着这个标签，测试需要用它构造「老数据」形态，
// 并断言新代码**忽略**它。所以它只剩在测试侧——生产代码里没有任何引用，
// 这正是「不再使用」的证明。
const projectionGenerationField = "projection_generation"
