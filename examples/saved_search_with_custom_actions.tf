resource "splunk_saved_searches" "custom_actions_example" {
  name               = "Custom Actions Alert Example"
  search             = "index=main error | stats count"
  actions            = "webhook"

  # Enable webhook action
  action_webhook = true
  action_webhook_enable_allowlist = false
  action_webhook_param_url = "https://your-webhook-endpoint.example.com/alerts"
  action_webhook_param_priority = 3
  action_webhook_param_mitre_attack_id = "[\"T1020\"]"
  action_webhook_param_description = "Example alert for demonstration"
  action_webhook_param_fields = "[\"event_utc_time\", \"src_ip\", \"user_name\"]"
  action_webhook_param_tags = "[\"Example\", \"Demo\", \"UEBA\"]"
  action_webhook_param_author = "Terraform Provider"

  # Alert configuration
  alert_type = "number of events"
  alert_comparator = "greater than"
  alert_threshold = "0"
  alert_digest_mode = true

  # Schedule the search to run every 5 minutes
  is_scheduled = true
  cron_schedule = "*/5 * * * *"

  # Other basic configuration
  description = "Example saved search with custom webhook and UBA actions"
  disabled = false
} 