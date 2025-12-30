package stats

import (
	"encoding/json"
	"io"

	"gopkg.in/yaml.v3"
)

// StatReportRender 定義輸出行為
type StatReportRender interface {
	Write(w io.Writer, r *StatReport) error
}

// Json渲染
type JsonStatReportRender struct{}

func (jr *JsonStatReportRender) Write(w io.Writer, r *StatReport) error {
	return json.NewEncoder(w).Encode(r)
}

// YAML渲染
type YAMLStatReportRender struct{}

func (yr *YAMLStatReportRender) Write(w io.Writer, r *StatReport) error {
	// 不管欄位，只要是陣列（YAML Sequence），就維持外層預設展開；
	// 只有「最內層的一維陣列」或「本身就是一維陣列」時才輸出成 flow style：[..., ...]
	return forceReadableList(w, r)
}

type EstimatorRender interface {
	Write(w io.Writer, e *EstimatorPlayers) error
}

// Json渲染
type JsonEstimatorRender struct{}

func (jr *JsonEstimatorRender) Write(w io.Writer, e *EstimatorPlayers) error {
	return json.NewEncoder(w).Encode(e)
}

// YAML渲染
type YAMLEstimatorRender struct{}

func (yr *YAMLEstimatorRender) Write(w io.Writer, e *EstimatorPlayers) error {
	// 不管欄位，只要是陣列（YAML Sequence），就維持外層預設展開；
	// 只有「最內層的一維陣列」或「本身就是一維陣列」時才輸出成 flow style：[..., ...]
	return forceReadableList(w, e)
}

// YAML 內層方法
func forceReadableList[T any](w io.Writer, t *T) error {
	var node yaml.Node
	if err := node.Encode(t); err != nil {
		return err
	}

	// 自頂向下調整所有 sequence node 的 style：
	// - 若該 sequence 內部「沒有子 sequence」，代表它是最內層的一維（或本身就是一維）=> 用 flow style: [...]
	// - 若該 sequence 內部「有子 sequence」，代表它是外層維度 => 保持預設 block（展開）
	styleReadableSequences(&node)

	enc := yaml.NewEncoder(w)
	defer enc.Close()
	return enc.Encode(&node)
}

func styleReadableSequences(n *yaml.Node) {
	if n == nil {
		return
	}

	switch n.Kind {
	case yaml.DocumentNode, yaml.MappingNode:
		for _, c := range n.Content {
			styleReadableSequences(c)
		}
		return

	case yaml.SequenceNode:
		// 先判斷這個 sequence 是否包含子 sequence（代表外層維度）
		hasChildSeq := false
		for _, c := range n.Content {
			if c != nil && c.Kind == yaml.SequenceNode {
				hasChildSeq = true
				break
			}
		}

		// 先遞迴處理子節點（讓最內層先被標記成 flow）
		for _, c := range n.Content {
			styleReadableSequences(c)
		}

		// 最內層一維（或本身就是一維）=> flow style: [a, b, c]
		// 外層維度 => 保持預設 block style（不強制設定 style）
		if !hasChildSeq {
			n.Style = yaml.FlowStyle
		}
		return

	default:
		// Scalar / Alias 等不處理
		return
	}
}
