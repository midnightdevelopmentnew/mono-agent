package service

import "monoagent/internal/workflow"

func RegisterGroupB(r *workflow.NodeTypeRegistry) {
	r.Register("service.stripe", func() workflow.NodeExecutor { return &StripeNode{} })
	r.Register("service.shopify", func() workflow.NodeExecutor { return &ShopifyNode{} })
	r.Register("service.salesforce", func() workflow.NodeExecutor { return &SalesforceNode{} })
	r.Register("service.hubspot", func() workflow.NodeExecutor { return &HubSpotNode{} })
	r.Register("service.google_sheets", func() workflow.NodeExecutor { return &GoogleSheetsNode{} })
	r.Register("service.gmail", func() workflow.NodeExecutor { return &GmailNode{} })
	r.Register("service.outlook_mail", func() workflow.NodeExecutor { return &OutlookMailNode{} })
	r.Register("service.google_drive", func() workflow.NodeExecutor { return &GoogleDriveNode{} })
	r.Register("service.youtube", func() workflow.NodeExecutor { return &YouTubeNode{} })
	r.Register("service.openrouter", func() workflow.NodeExecutor { return &OpenRouterNode{} })
	r.Register("service.huggingface", func() workflow.NodeExecutor { return &HuggingFaceNode{} })
}
