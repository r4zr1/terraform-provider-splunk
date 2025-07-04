package splunk

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/helper/validation"
	"github.com/splunk/terraform-provider-splunk/client/models"
)

func suppressDefault(defaultValue string) schema.SchemaDiffSuppressFunc {
	return func(k, old, new string, d *schema.ResourceData) bool {
		return old == defaultValue && new == ""
	}
}

func normalizeActionsString(actions string) string {
	if actions == "" {
		return ""
	}

	// Split by comma, trim whitespace, and sort
	actionList := strings.Split(actions, ",")
	for i, action := range actionList {
		actionList[i] = strings.TrimSpace(action)
	}

	// Remove empty strings
	var cleanActions []string
	for _, action := range actionList {
		if action != "" {
			cleanActions = append(cleanActions, action)
		}
	}

	sort.Strings(cleanActions)
	return strings.Join(cleanActions, ",")
}

func suppressActionsDiff(k, old, new string, d *schema.ResourceData) bool {
	return normalizeActionsString(old) == normalizeActionsString(new)
}

// calculateWebhookPriority calculates priority based on severity and precision
// following the business logic from the Python exporter
func calculateWebhookPriority(severity, precision string) int {
	switch severity {
	case "Critical":
		switch precision {
		case "High":
			return 4
		case "Medium":
			return 3
		case "Low":
			return 2
		}
	case "High":
		switch precision {
		case "High", "Medium":
			return 3
		case "Low":
			return 2
		}
	case "Medium":
		switch precision {
		case "High", "Medium":
			return 2
		case "Low":
			return 1
		}
	case "Low":
		return 1
	}
	// Default fallback
	return 1
}

// getCalculatedPriority returns either the manually set priority or auto-calculated one
func getCalculatedPriority(d *schema.ResourceData) int {
	// If priority is explicitly set, use it
	if priority, ok := d.GetOk("action_webhook_param_priority"); ok {
		return priority.(int)
	}

	// Otherwise, calculate from severity and precision
	severity := d.Get("severity").(string)
	precision := d.Get("precision").(string)

	if severity != "" && precision != "" {
		return calculateWebhookPriority(severity, precision)
	}

	// Default fallback
	return 1
}

func savedSearches() *schema.Resource {
	return &schema.Resource{
		Schema: map[string]*schema.Schema{
			"actions": {
				Type:             schema.TypeString,
				Optional:         true,
				Computed:         true,
				Description:      "A comma-separated list of actions to enable. For example: rss,email ",
				DiffSuppressFunc: suppressActionsDiff,
			},
			"action_snow_event_param_account": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Account(s) for which the event is/ are to be created across ServiceNow instance(s).",
			},
			"action_snow_event_param_node": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "The node, formatted to follow your organization's ITIL standards and mapping. If the node value matches a CI with the same host name, the event is automatically assigned to the matching CI.",
			},
			"action_snow_event_param_type": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "The type, formatted to follow your organization's ITIL standards and mapping. For example, type='Virtual Machine'.",
			},
			"action_snow_event_param_resource": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "The resource, formatted to follow your organization's ITIL standards and mapping. For example, resource='CPU'.",
			},
			"action_snow_event_param_severity": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
				Description: "The severity associated with the event. " +
					"0 - Clear " +
					"1 - Critical " +
					"2 - Major " +
					"3 - Minor " +
					"4 - Warning",
			},
			"action_snow_event_param_description": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "	A brief description of the event.",
			},
			"action_snow_event_param_ci_identifier": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "String that represents a configuration item in your network. You can pass value as || separated key-value format. For example, k1=v1||k2=v2.",
			},
			"action_snow_event_param_custom_fields": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "The custom fields which are configured at the ServiceNow Instance. " +
					"You can pass the custom fields and their values in the || separated format. For example, custom_field1=value1||custom_field2=value2||..." +
					"custom_fields used must be present in the em_event table of ServiceNow.",
			},
			"action_snow_event_param_additional_info": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "You can pass additional information that might be of use to the user. " +
					"This field can also be used to supply the URL of your Splunk search head. " +
					"When you use the snow_event.py alert-triggered script, the Splunk platform uses the URL to create a deep link that allows a ServiceNow user to navigate back to this Splunk platform search. " +
					"You can find the resulting full URL for navigation from ServiceNow to the Splunk platform search by clicking Splunk Drilldown in the event page in ServiceNow. See an example below. " +
					"Note that if you create events using the commands snowevent or snoweventstream, you must supply the URL in this field." +
					"You can pass the URL of Splunk as url=<value>. You can also pass other fields and their values by || separated key-value format. For example, url=<value>||k1=v1||k2=v2||....",
			},
			"action_email": {
				Type:     schema.TypeBool,
				Computed: true,
				Description: "The state of the email action. Read-only attribute. " +
					"Value ignored on POST. Use actions to specify a list of enabled actions. Defaults to 0.",
			},
			"action_email_auth_password": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "The password to use when authenticating with the SMTP server. " +
					"Normally this value is set when editing the email settings, however you can set a clear text password here and it is encrypted on the next platform restart." +
					"Defaults to empty string.",
			},
			"action_email_auth_username": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "The username to use when authenticating with the SMTP server. " +
					"If this is empty string, no authentication is attempted. Defaults to empty string" +
					"NOTE: Your SMTP server might reject unauthenticated emails.",
			},
			"action_email_bcc": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "BCC email address to use if action.email is enabled.",
			},
			"action_email_cc": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "CC email address to use if action.email is enabled.",
			},
			"action_email_command": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "The search command (or pipeline) which is responsible for executing the action." +
					"Generally the command is a template search pipeline which is realized with values from the saved search. " +
					"To reference saved search field values wrap them in $, for example to reference the savedsearch name use $name$, to reference the search use $search$. ",
			},
			"action_email_format": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Valid values: (table | plain | html | raw | csv)" +
					"Specify the format of text in the email. This value also applies to any attachments.",
			},
			"action_email_from": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Email address from which the email action originates." +
					"Defaults to splunk@$LOCALHOST or whatever value is set in alert_actions.conf.",
			},
			"action_email_hostname": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Sets the hostname used in the web link (url) sent in email actions." +
					"This value accepts two forms:hostname (for example, splunkserver, splunkserver.example.com) ",
			},
			"action_email_include_results_link": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Specify whether to include a link to the results. [1|0]",
			},
			"action_email_include_search": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Specify whether to include the search that caused an email to be sent. [1|0]",
			},
			"action_email_include_trigger": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Specify whether to show the trigger condition that caused the alert to fire. [1|0]",
			},
			"action_email_include_trigger_time": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Specify whether to show the time that the alert was fired. [1|0]",
			},
			"action_email_include_view_link": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Specify whether to show the title and a link to enable the user to edit the saved search. [1|0]",
			},
			"action_email_inline": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "Indicates whether the search results are contained in the body of the email." +
					"Results can be either inline or attached to an email. ",
			},
			"action_email_mailserver": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Set the address of the MTA server to be used to send the emails." +
					"Defaults to <LOCALHOST> or whatever is set in alert_actions.conf. ",
			},
			"action_email_max_results": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
				Description: "Sets the global maximum number of search results to send when email.action is enabled. " +
					"Defaults to 100.",
			},
			"action_email_max_time": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Valid values are Integer[m|s|h|d]." +
					"Specifies the maximum amount of time the execution of an email action takes before the action is aborted. Defaults to 5m.",
			},
			"action_email_message_alert": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Message sent in the emailed alert. Defaults to: The alert condition for '$name$' was triggered.",
			},
			"action_email_message_report": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Message sent in the emailed report. Defaults to: The scheduled report '$name$' has run.",
			},
			"action_email_pdfview": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "The name of the view to deliver if sendpdf is enabled",
			},
			"action_email_preprocess_results": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Search string to preprocess results before emailing them. Defaults to empty string (no preprocessing)." +
					"Usually the preprocessing consists of filtering out unwanted internal fields.",
			},
			"action_email_report_cid_font_list": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Space-separated list. Specifies the set (and load order) of CID fonts for handling Simplified Chinese(gb), Traditional Chinese(cns), Japanese(jp), and Korean(kor) in Integrated PDF Rendering." +
					"If multiple fonts provide a glyph for a given character code, the glyph from the first font specified in the list is used." +
					"To skip loading any CID fonts, specify the empty string.Defaults to 'gb cns jp kor'",
			},
			"action_email_report_include_splunk_logo": {
				Type:        schema.TypeBool,
				Optional:    true,
				Computed:    true,
				Description: "Indicates whether to include the Splunk logo with the report.",
			},
			"action_email_report_paper_orientation": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Valid values: (portrait | landscape)" +
					"Specifies the paper orientation: portrait or landscape. Defaults to portrait.",
			},
			"action_email_report_paper_size": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Valid values: (letter | legal | ledger | a2 | a3 | a4 | a5)" +
					"Specifies the paper size for PDFs. Defaults to letter.",
			},
			"action_email_report_server_enabled": {
				Type:        schema.TypeBool,
				Optional:    true,
				Computed:    true,
				Description: "No Supported",
			},
			"action_email_report_server_url": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Not supported." +
					"For a default locally installed report server, the URL is http://localhost:8091/",
			},
			"action_email_send_csv": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Specify whether to send results as a CSV file. Default: 0 (false).",
			},
			"action_email_send_pdf": {
				Type:        schema.TypeBool,
				Optional:    true,
				Computed:    true,
				Description: "Indicates whether to create and send the results as a PDF. Defaults to false.",
			},
			"action_email_send_results": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "Indicates whether to attach the search results in the email." +
					"Results can be either attached or inline. See action.email.inline.",
			},
			"action_email_allow_empty_attachment": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Indicates whether to allow empty attachments in the email.",
			},
			"action_email_subject": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Specifies an alternate email subject.Defaults to SplunkAlert-<savedsearchname>.",
			},
			"action_email_to": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "A comma or semicolon separated list of recipient email addresses. " +
					"Required if this search is scheduled and the email alert action is enabled.",
			},
			"action_email_track_alert": {
				Type:        schema.TypeBool,
				Optional:    true,
				Computed:    true,
				Description: "Indicates whether the execution of this action signifies a trackable alert.",
			},
			"action_email_ttl": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Valid values are Integer[p].Specifies the minimum time-to-live in seconds of the search artifacts if this action is triggered. " +
					"If p follows <Integer>, int is the number of scheduled periods. Defaults to 86400 (24 hours)." +
					"If no actions are triggered, the artifacts have their ttl determined by dispatch.ttl in savedsearches.conf.",
			},
			"action_email_use_ssl": {
				Type:        schema.TypeBool,
				Optional:    true,
				Computed:    true,
				Description: "Indicates whether to use SSL when communicating with the SMTP server. Defaults to false.",
			},
			"action_email_use_tls": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "Indicates whether to use TLS (transport layer security) when communicating with the SMTP server (starttls)." +
					"Defaults to false.",
			},
			"action_email_width_sort_columns": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "Indicates whether columns should be sorted from least wide to most wide, left to right." +
					"Only valid if format=text.",
			},
			"action_pagerduty_custom_details": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "The PagerDuty custom details information.",
			},
			"action_pagerduty_integration_key": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "The PagerDuty integration Key." +
					"NOTE: None.",
			},
			"action_pagerduty_integration_key_override": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "The PagerDuty integration Key override.",
			},
			"action_pagerduty_integration_url": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The pagerduty integration URL. This integration uses Splunk's native webhooks to send events to PagerDuty.",
			},
			"action_pagerduty_integration_url_override": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "The pagerduty integration URL override. This integration uses Splunk's native webhooks to send events to PagerDuty.",
			},
			"action_script": {
				Type:     schema.TypeBool,
				Computed: true,
				Description: "The state of the script action. Read-only attribute. Value ignored on POST. " +
					"Use actions to specify a list of enabled actions. Defaults to 0.",
			},
			"action_script_filename": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "File name of the script to call. Required if script action is enabled",
			},
			"action_create_xsoar_incident": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Enabled XSOAR Alert Sending.",
			},
			"action_create_xsoar_incident_param_send_all_servers": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Enabled XSOAR alert sending to all servers.",
			},
			"action_create_xsoar_incident_param_server_url": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Enter the XSOAR server URL.",
			},
			"action_create_xsoar_incident_param_incident_name": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Enter the XSOAR incident name.",
			},
			"action_create_xsoar_incident_param_details": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Enter the XSOAR incident details.",
			},
			"action_create_xsoar_incident_param_custom_fields": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Enter the XSOAR incident custom_fields.",
			},
			"action_create_xsoar_incident_param_severity": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Enter the XSOAR incident serverity.",
			},
			"action_create_xsoar_incident_param_occurred": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Eneter the XSOAR incident occurred datetime.",
			},
			"action_create_xsoar_incident_param_type": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Enter the XSOAR incident type.",
			},
			"action_slack_param_channel": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Slack channel to send the message to (Should start with # or @)",
			},
			"action_slack_param_fields": {
				Type:     schema.TypeString,
				Optional: true,
				Description: "Show one or more fields from the search results below your Slack message. " +
					"Comma-separated list of field names. Allows wildcards. eg. index,source*",
			},
			"action_slack_param_attachment": {
				Type:        schema.TypeString,
				Optional:    true,
				Default:     "none",
				Description: "Optionally include a message attachment. Valid values are message, alert_link, or none",
			},
			"action_slack_param_message": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Enter the chat message to send to the Slack channel. The message can include tokens that insert text based on the results of the search.",
			},
			"action_slack_param_webhook_url_override": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "You can override the Slack webhook URL here if you need to send the alert message to a different Slack team.",
			},
			"action_jira_service_desk_param_account": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "This is where you would put the account name you set in the Jira Service Desk",
			},
			"action_jira_service_desk_param_jira_project": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Jira Project where the issue will be created",
			},
			"action_jira_service_desk_param_jira_issue_type": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Jira Issue Type you would like to create",
			},
			"action_jira_service_desk_param_jira_summary": {
				Type:             schema.TypeString,
				Optional:         true,
				Description:      "Jira Issue Summary or title",
				DiffSuppressFunc: suppressDefault("Splunk Alert: $name$"),
			},
			"action_jira_service_desk_param_jira_priority": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Priority of issue created",
			},
			"action_jira_service_desk_param_jira_description": {
				Type:             schema.TypeString,
				Optional:         true,
				Description:      "Enter the description of issue created",
				DiffSuppressFunc: suppressDefault("The alert condition for '$name$' was triggered."),
			},
			"action_jira_service_desk_param_jira_customfields": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Enter custom fields data for the issue created",
			},
			"action_webhook_param_url": {
				Type:         schema.TypeString,
				Optional:     true,
				Description:  "URL to send the HTTP POST request to. Must be accessible from the Splunk server.",
				ValidateFunc: validation.StringMatch(regexp.MustCompile(`^https?://[^\s]+$`), "Webhook URL is invalid"),
			},
			"action_webhook": {
				Type:        schema.TypeBool,
				Computed:    true,
				Description: "The state of the webhook action. Automatically determined from actions field.",
			},
			"action_webhook_enable_allowlist": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Enable webhook allowlist for this alert action.",
			},
			"action_webhook_param_priority": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Priority parameter for webhook action. If not set, will be auto-calculated from severity and precision.",
			},
			"severity": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				Description:  "Alert severity level (Critical, High, Medium, Low). Used to auto-calculate webhook priority.",
				ValidateFunc: validation.StringInSlice([]string{"Critical", "High", "Medium", "Low"}, false),
			},
			"precision": {
				Type:         schema.TypeString,
				Optional:     true,
				Computed:     true,
				Description:  "Alert precision level (High, Medium, Low). Used to auto-calculate webhook priority.",
				ValidateFunc: validation.StringInSlice([]string{"High", "Medium", "Low"}, false),
			},
			"action_webhook_param_mitre_attack_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "MITRE ATT&CK technique IDs associated with this alert.",
			},
			"action_webhook_param_description": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Description parameter for webhook action.",
			},
			"action_webhook_param_fields": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Fields parameter for webhook action.",
			},
			"action_webhook_param_tags": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Tags parameter for webhook action.",
			},
			"action_webhook_param_author": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Author parameter for webhook action.",
			},
			"alert_digest_mode": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "Specifies whether alert actions are applied to the entire result set or on each individual result." +
					"Defaults to 1 (true).",
			},
			"alert_expires": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Valid values: [number][time-unit]Sets the period of time to show the alert in the dashboard. Defaults to 24h." +
					"Use [number][time-unit] to specify a time. " +
					"For example: 60 = 60 seconds, 1m = 1 minute, 1h = 60 minutes = 1 hour.",
			},
			"alert_severity": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
				Description: "Valid values: (1 | 2 | 3 | 4 | 5 | 6) Sets the alert severity level." +
					"Valid values are:1 DEBUG 2 INFO 3 WARN 4 ERROR 5 SEVERE 6 FATAL Defaults to 3.",
			},
			"alert_suppress": {
				Type:        schema.TypeBool,
				Optional:    true,
				Computed:    true,
				Description: "Indicates whether alert suppression is enabled for this scheduled search.",
			},
			"alert_suppress_fields": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Comma delimited list of fields to use for suppression when doing per result alerting. " +
					"Required if suppression is turned on and per result alerting is enabled.",
			},
			"alert_suppress_period": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Valid values: [number][time-unit] Specifies the suppresion period. Only valid if alert.supress is enabled." +
					"Use [number][time-unit] to specify a time. For example: 60 = 60 seconds, 1m = 1 minute, 1h = 60 minutes = 1 hour. ",
			},
			"alert_track": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "Valid values: (true | false | auto) Specifies whether to track the actions triggered by this scheduled search." +
					"auto - determine whether to track or not based on the tracking setting of each action, do not track scheduled searches that always trigger actions. " +
					"Default value true - force alert tracking.false - disable alert tracking for this search.",
			},
			"alert_comparator": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "One of the following strings: greater than, less than, equal to, rises by, drops by, rises by perc, drops by perc" +
					"Used with alert_threshold to trigger alert actions.",
			},
			"alert_condition": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Contains a conditional search that is evaluated against the results of the saved search. " +
					"Defaults to an empty string.",
			},
			"alert_threshold": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Valid values are: Integer[%]" +
					"Specifies the value to compare (see alert_comparator) before triggering the alert actions. " +
					"If expressed as a percentage, indicates value to use when alert_comparator is set to rises by perc or drops by perc.",
			},
			"alert_type": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "What to base the alert on, overriden by alert_condition if it is specified. " +
					"Valid values are: always, custom, number of events, number of hosts, number of sources.",
			},
			"allow_skew": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Allows the search scheduler to distribute scheduled searches randomly and more evenly over their specified search periods.",
			},
			"auto_summarize": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "Indicates whether the scheduler should ensure that the data for this search is automatically summarized. " +
					"Defaults to 0.",
			},
			"auto_summarize_command": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "An auto summarization template for this search. " +
					"See auto summarization options in savedsearches.conf for more details.",
			},
			"auto_summarize_cron_schedule": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Cron schedule that probes and generates the summaries for this saved search." +
					"The default value is */10 * * * * and corresponds to \"every ten hours\".",
			},
			"auto_summarize_dispatch_earliest_time": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "A time string that specifies the earliest time for summarizing this search. Can be a relative or absolute time." +
					"If this value is an absolute time, use the dispatch.time_format to format the value.",
			},
			"auto_summarize_dispatch_latest_time": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "A time string that specifies the latest time for summarizing this saved search. Can be a relative or absolute time." +
					"If this value is an absolute time, use the dispatch.time_format to format the value.",
			},
			"auto_summarize_dispatch_time_format": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Defines the time format that Splunk software uses to specify the earliest and latest time. Defaults to %FT%T.%Q%:z",
			},
			"auto_summarize_dispatch_ttl": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Valid values: Integer[p]. Defaults to 60." +
					"Indicates the time to live (in seconds) for the artifacts of the summarization of the scheduled search. ",
			},
			"auto_summarize_max_disabled_buckets": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
				Description: "The maximum number of buckets with the suspended summarization before the summarization search is completely stopped, " +
					"and the summarization of the search is suspended for auto_summarize.suspend_period. Defaults to 2.",
			},
			"auto_summarize_max_summary_ratio": {
				Type:     schema.TypeFloat,
				Optional: true,
				Computed: true,
				Description: "The maximum ratio of summary_size/bucket_size, which specifies when to stop summarization and deem it unhelpful for a bucket. " +
					"Defaults to 0.1 Note: The test is only performed if the summary size is larger than auto_summarize.max_summary_size.",
			},
			"auto_summarize_max_summary_size": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
				Description: "The minimum summary size, in bytes, before testing whether the summarization is helpful." +
					"The default value is 52428800 and is equivalent to 5MB. ",
			},
			"auto_summarize_max_time": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
				Description: "Maximum time (in seconds) that the summary search is allowed to run. Defaults to 3600." +
					"Note: This is an approximate time. The summary search stops at clean bucket boundaries. ",
			},
			"auto_summarize_suspend_period": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Time specfier indicating when to suspend summarization of this search if the summarization is deemed unhelpful." +
					"Defaults to 24h. ",
			},
			"auto_summarize_timespan": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "The list of time ranges that each summarized chunk should span. " +
					"This comprises the list of available granularity levels for which summaries would be available. " +
					"Specify a comma delimited list of time specifiers." +
					"For example a timechart over the last month whose granuality is at the day level should set this to 1d. If you need the same data summarized at the hour level for weekly charts, use: 1h,1d. ",
			},
			"cron_schedule": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Valid values: cron string" +
					"The cron schedule to execute this search. " +
					"For example: */5 * * * * causes the search to execute every 5 minutes. ",
			},
			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Human-readable description of this saved search. Defaults to empty string. ",
			},
			"disabled": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "Indicates if the saved search is enabled. Defaults to 0." +
					"Disabled saved searches are not visible in Splunk Web. ",
			},
			"dispatch_buckets": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "The maximum number of timeline buckets. Defaults to 0. ",
			},
			"dispatch_earliest_time": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "A time string that specifies the earliest time for this search. Can be a relative or absolute time." +
					"If this value is an absolute time, use the dispatch.time_format to format the value.",
			},
			"dispatch_index_earliest": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "A time string that specifies the latest time for this search. Can be a relative or absolute time." +
					"If this value is an absolute time, use the dispatch.time_format to format the value.",
			},
			"dispatch_index_latest": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "A time string that specifies the earliest time for this search. Can be a relative or absolute time." +
					"If this value is an absolute time, use the dispatch.time_format to format the value.",
			},
			"dispatch_indexed_realtime": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "A time string that specifies the earliest time for this search. Can be a relative or absolute time." +
					"If this value is an absolute time, use the dispatch.time_format to format the value. ",
			},
			"dispatch_indexed_realtime_offset": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Allows for a per-job override of the [search] indexed_realtime_disk_sync_delay setting in limits.conf.",
			},
			"dispatch_indexed_realtime_minspan": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Allows for a per-job override of the [search] indexed_realtime_disk_sync_delay setting in limits.conf.",
			},
			"dispatch_latest_time": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "A time string that specifies the latest time for this saved search. Can be a relative or absolute time." +
					"If this value is an absolute time, use the dispatch.time_format to format the value.",
			},
			"dispatch_lookups": {
				Type:        schema.TypeBool,
				Optional:    true,
				Computed:    true,
				Description: "Enables or disables the lookups for this search. Defaults to 1. ",
			},
			"dispatch_max_count": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "The maximum number of results before finalizing the search. Defaults to 500000. ",
			},
			"dispatch_max_time": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Indicates the maximum amount of time (in seconds) before finalizing the search. Defaults to 0. ",
			},
			"dispatch_reduce_freq": {
				Type:     schema.TypeInt,
				Optional: true,
				Computed: true,
				Description: "Specifies, in seconds, how frequently the MapReduce reduce phase runs on accumulated map values. " +
					"Defaults to 10.",
			},
			"dispatch_rt_backfill": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "Whether to back fill the real time window for this search." +
					" Parameter valid only if this is a real time search. Defaults to 0.",
			},
			"dispatch_rt_maximum_span": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "Allows for a per-job override of the [search] indexed_realtime_maximum_span setting in limits.conf.",
			},
			"dispatch_spawn_process": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "Specifies whether a new search process spawns when this saved search is executed. " +
					"Defaults to 1. Searches against indexes must run in a separate process. ",
			},
			"dispatch_time_format": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "A time format string that defines the time format for specifying the earliest and latest time. " +
					"Defaults to %FT%T.%Q%:z",
			},
			"dispatch_ttl": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Valid values: Integer[p]. Defaults to 2p." +
					"Indicates the time to live (in seconds) for the artifacts of the scheduled search, if no actions are triggered. ",
			},
			"display_view": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Defines the default UI view name (not label) in which to load the results. " +
					"Accessibility is subject to the user having sufficient permissions.",
			},
			"is_scheduled": {
				Type:        schema.TypeBool,
				Optional:    true,
				Computed:    true,
				Description: "Whether this search is to be run on a schedule ",
			},
			"is_visible": {
				Type:        schema.TypeBool,
				Default:     true,
				Optional:    true,
				Description: "Specifies whether this saved search should be listed in the visible saved search list. Defaults to 1. ",
			},
			"max_concurrent": {
				Type:        schema.TypeInt,
				Optional:    true,
				Computed:    true,
				Description: "The maximum number of concurrent instances of this search the scheduler is allowed to run. Defaults to 1. ",
			},
			"name": {
				Type:        schema.TypeString,
				ForceNew:    true,
				Required:    true,
				Description: "A name for the search.",
			},
			"realtime_schedule": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "Defaults to 1. Controls the way the scheduler computes the next execution time of a scheduled search. " +
					"If this value is set to 1, the scheduler bases its determination of the next scheduled search execution time on the current time. " +
					"If this value is set to 0, the scheduler bases its determination of the next scheduled search on the last search execution time. " +
					"This is called continuous scheduling. If set to 0, the scheduler never skips scheduled execution periods. " +
					"However, the execution of the saved search might fall behind depending on the scheduler load. " +
					"Use continuous scheduling whenever you enable the summary index option.",
			},
			"request_ui_dispatch_app": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Specifies a field used by Splunk Web to denote the app this search should be dispatched in. ",
			},
			"request_ui_dispatch_view": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Specifies a field used by Splunk Web to denote the view this search should be displayed in. ",
			},
			"restart_on_searchpeer_add": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "Specifies whether to restart a real-time search managed by the scheduler when a search peer becomes available for this saved search. " +
					"Defaults to 1. ",
			},
			"run_on_startup": {
				Type:     schema.TypeBool,
				Optional: true,
				Computed: true,
				Description: "Indicates whether this search runs at startup. " +
					"If it does not run on startup, it runs at the next scheduled time. " +
					"Defaults to 0. Set to 1 for scheduled searches that populate lookup tables. ",
			},
			"schedule_window": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Time window (in minutes) during which the search has lower priority. Defaults to 0. " +
					"The scheduler can give higher priority to more critical searches during this window. " +
					"The window must be smaller than the search period." +
					"Set to auto to let the scheduler determine the optimal window value automatically. " +
					"Requires the edit_search_schedule_window capability to override auto. ",
			},
			"schedule_priority": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Raises the scheduling priority of the named search. Defaults to Default",
			},
			"search": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Required when creating a new search",
			},
			"vsid": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Defines the viewstate id associated with the UI view listed in 'displayview'. ",
			},
			"workload_pool": {
				Type:        schema.TypeString,
				Optional:    true,
				Computed:    true,
				Description: "Specifies the new workload pool where the existing running search will be placed.",
			},
			"acl": aclSchema(),
		},
		SchemaVersion: 1,
		StateUpgraders: []schema.StateUpgrader{
			{
				Type:    resourceAlertTrackV0().CoreConfigSchema().ImpliedType(),
				Upgrade: resourceAlertTrackStateUpgradeV0,
				Version: 0,
			},
		},
		Create: savedSearchesCreate,
		Read:   savedSearchesRead,
		Update: savedSearchesUpdate,
		Delete: savedSearchesDelete,
		Importer: &schema.ResourceImporter{
			State: schema.ImportStatePassthrough,
		},
	}

}

func resourceAlertTrackV0() *schema.Resource {
	return &schema.Resource{
		Schema: map[string]*schema.Schema{
			"alert_track": {
				Type:     schema.TypeString,
				Optional: true,
				Computed: true,
				Description: "Valid values: (true | false | auto) Specifies whether to track the actions triggered by this scheduled search." +
					"auto - determine whether to track or not based on the tracking setting of each action, do not track scheduled searches that always trigger actions. " +
					"Default value true - force alert tracking.false - disable alert tracking for this search.",
			},
		},
	}
}

func resourceAlertTrackStateUpgradeV0(rawState map[string]interface{}, meta interface{}) (map[string]interface{}, error) {
	rawState["alert_track"], _ = strconv.ParseBool(rawState["alert_track"].(string))
	return rawState, nil
}

func savedSearchesCreate(d *schema.ResourceData, meta interface{}) error {
	provider := meta.(*SplunkProvider)
	name := d.Get("name").(string)
	savedSearchesConfig := getSavedSearchesConfig(d)
	aclObject := getResourceDataSearchACL(d)
	err := (*provider.Client).CreateSavedSearches(name, aclObject.Owner, aclObject.App, savedSearchesConfig)
	if err != nil {
		return err
	}

	if _, ok := d.GetOk("acl"); ok {
		if err := (*provider.Client).UpdateAcl(aclObject.Owner, aclObject.App, name, aclObject, "saved", "searches"); err != nil {
			return fmt.Errorf("savedSearchesCreate: updateacl: %s\n%#v", err, aclObject)
		}
	}

	d.SetId(name)
	return savedSearchesRead(d, meta)
}

func savedSearchesRead(d *schema.ResourceData, meta interface{}) error {
	provider := meta.(*SplunkProvider)
	name := d.Id()

	aclObject := getResourceDataSearchACL(d)

	resp, err := (*provider.Client).ReadSavedSearches(name, aclObject.Owner, aclObject.App)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	entry, err := getSavedSearchesConfigByName(name, resp)
	if err != nil {
		return err
	}

	if entry == nil {
		return fmt.Errorf("Unable to find resource: %v", name)
	}

	if err = d.Set("name", d.Id()); err != nil {
		return err
	}
	if err = d.Set("actions", normalizeActionsString(entry.Content.Actions)); err != nil {
		return err
	}
	if err = d.Set("action_snow_event_param_account", entry.Content.ActionSnowEventParamAccount); err != nil {
		return err
	}
	if err = d.Set("action_snow_event_param_node", entry.Content.ActionSnowEventParamNode); err != nil {
		return err
	}
	if err = d.Set("action_snow_event_param_type", entry.Content.ActionSnowEventParamType); err != nil {
		return err
	}
	if err = d.Set("action_snow_event_param_resource", entry.Content.ActionSnowEventParamResource); err != nil {
		return err
	}
	if err = d.Set("action_snow_event_param_severity", entry.Content.ActionSnowEventParamSeverity); err != nil {
		return err
	}
	if err = d.Set("action_snow_event_param_description", entry.Content.ActionSnowEventParamDescription); err != nil {
		return err
	}
	if err = d.Set("action_snow_event_param_ci_identifier", entry.Content.ActionSnowEventParamCiIdentifier); err != nil {
		return err
	}
	if err = d.Set("action_snow_event_param_custom_fields", entry.Content.ActionSnowEventParamCustomFields); err != nil {
		return err
	}
	if err = d.Set("action_snow_event_param_additional_info", entry.Content.ActionSnowEventParamAdditionalInfo); err != nil {
		return err
	}
	if err = d.Set("action_email", entry.Content.ActionEmail); err != nil {
		return err
	}
	if err = d.Set("action_email_auth_password", entry.Content.ActionEmailAuthPassword); err != nil {
		return err
	}
	if err = d.Set("action_email_auth_username", entry.Content.ActionEmailAuthUsername); err != nil {
		return err
	}
	if err = d.Set("action_email_bcc", entry.Content.ActionEmailBCC); err != nil {
		return err
	}
	if err = d.Set("action_email_cc", entry.Content.ActionEmailCC); err != nil {
		return err
	}
	if err = d.Set("action_email_command", entry.Content.ActionEmailCommand); err != nil {
		return err
	}
	if err = d.Set("action_email_format", entry.Content.ActionEmailFormat); err != nil {
		return err
	}
	if err = d.Set("action_email_from", entry.Content.ActionEmailFrom); err != nil {
		return err
	}
	if err = d.Set("action_email_include_results_link", entry.Content.ActionEmailIncludeResultsLink); err != nil {
		return err
	}
	if err = d.Set("action_email_include_search", entry.Content.ActionEmailIncludeSearch); err != nil {
		return err
	}
	if err = d.Set("action_email_include_trigger", entry.Content.ActionEmailIncludeTrigger); err != nil {
		return err
	}
	if err = d.Set("action_email_include_trigger_time", entry.Content.ActionEmailIncludeTriggerTime); err != nil {
		return err
	}
	if err = d.Set("action_email_include_view_link", entry.Content.ActionEmailIncludeViewLink); err != nil {
		return err
	}
	if err = d.Set("action_email_inline", entry.Content.ActionEmailInline); err != nil {
		return err
	}
	if err = d.Set("action_email_mailserver", entry.Content.ActionEmailMailserver); err != nil {
		return err
	}
	if err = d.Set("action_email_max_results", entry.Content.ActionEmailMaxResults); err != nil {
		return err
	}
	if err = d.Set("action_email_max_time", entry.Content.ActionEmailMaxTime); err != nil {
		return err
	}
	if err = d.Set("action_email_message_alert", entry.Content.ActionEmailMessageAlert); err != nil {
		return err
	}
	if err = d.Set("action_email_message_report", entry.Content.ActionEmailMessageReport); err != nil {
		return err
	}
	if err = d.Set("action_email_pdfview", entry.Content.ActionEmailPDFView); err != nil {
		return err
	}
	if err = d.Set("action_email_preprocess_results", entry.Content.ActionEmailPreprocessResults); err != nil {
		return err
	}
	if err = d.Set("action_email_report_cid_font_list", entry.Content.ActionEmailReportCIDFontList); err != nil {
		return err
	}
	if err = d.Set("action_email_report_include_splunk_logo", entry.Content.ActionEmailReportIncludeSplunkLogo); err != nil {
		return err
	}
	if err = d.Set("action_email_report_paper_orientation", entry.Content.ActionEmailReportPaperOrientation); err != nil {
		return err
	}
	if err = d.Set("action_email_report_paper_size", entry.Content.ActionEmailReportPaperSize); err != nil {
		return err
	}
	if err = d.Set("action_email_report_server_enabled", entry.Content.ActionEmailReportServerEnabled); err != nil {
		return err
	}
	if err = d.Set("action_email_report_server_url", entry.Content.ActionEmailReportServerURL); err != nil {
		return err
	}
	if err = d.Set("action_email_send_csv", entry.Content.ActionEmailSendCSV); err != nil {
		return err
	}
	if err = d.Set("action_email_send_pdf", entry.Content.ActionEmailSendPDF); err != nil {
		return err
	}
	if err = d.Set("action_email_send_results", entry.Content.ActionEmailSendResults); err != nil {
		return err
	}
	if err = d.Set("action_email_allow_empty_attachment", entry.Content.ActionEmailAllowEmptyAttachment); err != nil {
		return err
	}
	if err = d.Set("action_email_subject", entry.Content.ActionEmailSubject); err != nil {
		return err
	}
	if err = d.Set("action_email_to", entry.Content.ActionEmailTo); err != nil {
		return err
	}
	if err = d.Set("action_email_track_alert", entry.Content.ActionEmailTrackAlert); err != nil {
		return err
	}
	if err = d.Set("action_email_ttl", entry.Content.ActionEmailTTL); err != nil {
		return err
	}
	if err = d.Set("action_email_use_ssl", entry.Content.ActionEmailUseSSL); err != nil {
		return err
	}
	if err = d.Set("action_email_use_tls", entry.Content.ActionEmailUseTLS); err != nil {
		return err
	}
	if err = d.Set("action_email_width_sort_columns", entry.Content.ActionEmailWidthSortColumns); err != nil {
		return err
	}
	if err = d.Set("action_pagerduty_integration_url", entry.Content.ActionPagerdutyIntegrationURL); err != nil {
		return err
	}
	if err = d.Set("action_script", entry.Content.ActionScript); err != nil {
		return err
	}
	if err = d.Set("action_script_filename", entry.Content.ActionScriptFilename); err != nil {
		return err
	}
	if err = d.Set("action_create_xsoar_incident", entry.Content.ActionCreateXsoarIncident); err != nil {
		return err
	}
	if err = d.Set("action_create_xsoar_incident_param_send_all_servers", entry.Content.ActionCreateXsoarIncidentParamSendAllServers); err != nil {
		return err
	}
	if err = d.Set("action_create_xsoar_incident_param_server_url", entry.Content.ActionCreateXsoarIncidentParamServerUrl); err != nil {
		return err
	}
	if err = d.Set("action_create_xsoar_incident_param_incident_name", entry.Content.ActionCreateXsoarIncidentParamIncidentName); err != nil {
		return err
	}
	if err = d.Set("action_create_xsoar_incident_param_details", entry.Content.ActionCreateXsoarIncidentParamDetails); err != nil {
		return err
	}
	if err = d.Set("action_create_xsoar_incident_param_custom_fields", entry.Content.ActionCreateXsoarIncidentParamCustomFields); err != nil {
		return err
	}
	if err = d.Set("action_create_xsoar_incident_param_severity", entry.Content.ActionCreateXsoarIncidentParamSeverity); err != nil {
		return err
	}
	if err = d.Set("action_create_xsoar_incident_param_occurred", entry.Content.ActionCreateXsoarIncidentParamOccurred); err != nil {
		return err
	}
	if err = d.Set("action_create_xsoar_incident_param_type", entry.Content.ActionCreateXsoarIncidentParamType); err != nil {
		return err
	}
	if err = d.Set("action_slack_param_attachment", entry.Content.ActionSlackParamAttachment); err != nil {
		return err
	}
	if err = d.Set("action_slack_param_channel", entry.Content.ActionSlackParamChannel); err != nil {
		return err
	}
	if err = d.Set("action_slack_param_fields", entry.Content.ActionSlackParamFields); err != nil {
		return err
	}
	if err = d.Set("action_slack_param_message", entry.Content.ActionSlackParamMessage); err != nil {
		return err
	}
	if err = d.Set("action_slack_param_webhook_url_override", entry.Content.ActionSlackParamWebhookUrlOverride); err != nil {
		return err
	}
	if err = d.Set("action_jira_service_desk_param_account", entry.Content.ActionJiraServiceDeskParamAccount); err != nil {
		return err
	}
	if err = d.Set("action_jira_service_desk_param_jira_project", entry.Content.ActionJiraServiceDeskParamJiraProject); err != nil {
		return err
	}
	if err = d.Set("action_jira_service_desk_param_jira_issue_type", entry.Content.ActionJiraServiceDeskParamJiraIssueType); err != nil {
		return err
	}
	if entry.Content.ActionJiraServiceDeskParamJiraSummary != "Splunk Alert: $name$" {
		if err = d.Set("action_jira_service_desk_param_jira_summary", entry.Content.ActionJiraServiceDeskParamJiraSummary); err != nil {
			return err
		}
	}
	if err = d.Set("action_jira_service_desk_param_jira_priority", entry.Content.ActionJiraServiceDeskParamJiraPriority); err != nil {
		return err
	}
	if entry.Content.ActionJiraServiceDeskParamJiraDescription != "The alert condition for '$name$' was triggered." {
		if err = d.Set("action_jira_service_desk_param_jira_description", entry.Content.ActionJiraServiceDeskParamJiraDescription); err != nil {
			return err
		}
	}
	if err = d.Set("action_jira_service_desk_param_jira_customfields", entry.Content.ActionJiraServiceDeskParamJiraCustomfields); err != nil {
		return err
	}
	if err = d.Set("action_webhook_param_url", entry.Content.ActionWebhookParamUrl); err != nil {
		return err
	}
	if err = d.Set("action_webhook", entry.Content.ActionWebhook); err != nil {
		return err
	}
	if err = d.Set("action_webhook_enable_allowlist", entry.Content.ActionWebhookEnableAllowlist); err != nil {
		return err
	}
	if err = d.Set("action_webhook_param_priority", entry.Content.ActionWebhookParamPriority); err != nil {
		return err
	}
	if err = d.Set("action_webhook_param_mitre_attack_id", entry.Content.ActionWebhookParamMitreAttackId); err != nil {
		return err
	}
	if err = d.Set("action_webhook_param_description", entry.Content.ActionWebhookParamDescription); err != nil {
		return err
	}
	if err = d.Set("action_webhook_param_fields", entry.Content.ActionWebhookParamFields); err != nil {
		return err
	}
	if err = d.Set("action_webhook_param_tags", entry.Content.ActionWebhookParamTags); err != nil {
		return err
	}
	if err = d.Set("action_webhook_param_author", entry.Content.ActionWebhookParamAuthor); err != nil {
		return err
	}
	if err = d.Set("alert_digest_mode", entry.Content.AlertDigestMode); err != nil {
		return err
	}
	if err = d.Set("alert_expires", entry.Content.AlertExpires); err != nil {
		return err
	}
	if err = d.Set("alert_severity", entry.Content.AlertSeverity); err != nil {
		return err
	}
	if err = d.Set("alert_suppress", entry.Content.AlertSuppress); err != nil {
		return err
	}
	if err = d.Set("alert_suppress_fields", entry.Content.AlertSuppressFields); err != nil {
		return err
	}
	if err = d.Set("alert_suppress_period", entry.Content.AlertSuppressPeriod); err != nil {
		return err
	}
	if err = d.Set("alert_track", entry.Content.AlertTrack); err != nil {
		return err
	}
	if err = d.Set("alert_comparator", entry.Content.AlertComparator); err != nil {
		return err
	}
	if err = d.Set("alert_condition", entry.Content.AlertCondition); err != nil {
		return err
	}
	if err = d.Set("alert_threshold", entry.Content.AlertThreshold); err != nil {
		return err
	}
	if err = d.Set("alert_type", entry.Content.AlertType); err != nil {
		return err
	}
	if err = d.Set("allow_skew", entry.Content.AllowSkew); err != nil {
		return err
	}

	if err = d.Set("auto_summarize", entry.Content.AutoSummarize); err != nil {
		return err
	}
	if err = d.Set("auto_summarize_command", entry.Content.AutoSummarizeCommand); err != nil {
		return err
	}
	if err = d.Set("auto_summarize_cron_schedule", entry.Content.AutoSummarizeCronSchedule); err != nil {
		return err
	}
	if err = d.Set("auto_summarize_dispatch_earliest_time", entry.Content.AutoSummarizeDispatchEarliestTime); err != nil {
		return err
	}
	if err = d.Set("auto_summarize_dispatch_latest_time", entry.Content.AutoSummarizeDispatchLatestTime); err != nil {
		return err
	}
	if err = d.Set("auto_summarize_dispatch_time_format", entry.Content.AutoSummarizeDispatchTimeFormat); err != nil {
		return err
	}
	if err = d.Set("auto_summarize_dispatch_ttl", entry.Content.AutoSummarizeDispatchTTL); err != nil {
		return err
	}
	if err = d.Set("auto_summarize_max_disabled_buckets", entry.Content.AutoSummarizeMaxDisabledBuckets); err != nil {
		return err
	}
	if err = d.Set("auto_summarize_max_summary_ratio", entry.Content.AutoSummarizeMaxSummaryRatio); err != nil {
		return err
	}
	if err = d.Set("auto_summarize_max_summary_size", entry.Content.AutoSummarizeMaxSummarySize); err != nil {
		return err
	}
	if err = d.Set("auto_summarize_max_time", entry.Content.AutoSummarizeMaxTime); err != nil {
		return err
	}
	if err = d.Set("auto_summarize_suspend_period", entry.Content.AutoSummarizeSuspendPeriod); err != nil {
		return err
	}
	if err = d.Set("auto_summarize_timespan", entry.Content.AutoSummarizeTimespan); err != nil {
		return err
	}
	if err = d.Set("cron_schedule", entry.Content.CronSchedule); err != nil {
		return err
	}
	if err = d.Set("description", entry.Content.Description); err != nil {
		return err
	}
	if err = d.Set("disabled", entry.Content.Disabled); err != nil {
		return err
	}
	if err = d.Set("dispatch_buckets", entry.Content.DispatchBuckets); err != nil {
		return err
	}
	if err = d.Set("dispatch_earliest_time", entry.Content.DispatchEarliestTime); err != nil {
		return err
	}
	if err = d.Set("dispatch_index_earliest", entry.Content.DispatchIndexEarliest); err != nil {
		return err
	}
	if err = d.Set("dispatch_index_latest", entry.Content.DispatchIndexLatest); err != nil {
		return err
	}
	if err = d.Set("dispatch_indexed_realtime", entry.Content.DispatchIndexedRealtime); err != nil {
		return err
	}
	if err = d.Set("dispatch_indexed_realtime_offset", entry.Content.DispatchIndexedRealtimeOffset); err != nil {
		return err
	}
	if err = d.Set("dispatch_indexed_realtime_minspan", entry.Content.DispatchIndexedRealtimeMinspan); err != nil {
		return err
	}
	if err = d.Set("dispatch_latest_time", entry.Content.DispatchLatestTime); err != nil {
		return err
	}
	if err = d.Set("dispatch_lookups", entry.Content.DispatchLookups); err != nil {
		return err
	}
	if err = d.Set("dispatch_max_count", entry.Content.DispatchMaxCount); err != nil {
		return err
	}
	if err = d.Set("dispatch_max_time", entry.Content.DispatchMaxTime); err != nil {
		return err
	}
	if err = d.Set("dispatch_reduce_freq", entry.Content.DispatchReduceFreq); err != nil {
		return err
	}
	if err = d.Set("dispatch_rt_backfill", entry.Content.DispatchRtBackfill); err != nil {
		return err
	}
	if err = d.Set("dispatch_rt_maximum_span", entry.Content.DispatchRtMaximumSpan); err != nil {
		return err
	}
	if err = d.Set("dispatch_spawn_process", entry.Content.DispatchSpawnProcess); err != nil {
		return err
	}
	if err = d.Set("dispatch_time_format", entry.Content.DispatchTimeFormat); err != nil {
		return err
	}
	if err = d.Set("dispatch_ttl", entry.Content.DispatchTTL); err != nil {
		return err
	}
	if err = d.Set("display_view", entry.Content.DisplayView); err != nil {
		return err
	}
	if err = d.Set("is_scheduled", entry.Content.IsScheduled); err != nil {
		return err
	}
	if err = d.Set("is_visible", entry.Content.IsVisible); err != nil {
		return err
	}
	if err = d.Set("max_concurrent", entry.Content.MaxConcurrent); err != nil {
		return err
	}
	if err = d.Set("realtime_schedule", entry.Content.RealtimeSchedule); err != nil {
		return err
	}
	if err = d.Set("request_ui_dispatch_app", entry.Content.RequestUIDispatchApp); err != nil {
		return err
	}
	if err = d.Set("request_ui_dispatch_view", entry.Content.RequestUIDispatchView); err != nil {
		return err
	}
	if err = d.Set("restart_on_searchpeer_add", entry.Content.RestartOnSearchPeerAdd); err != nil {
		return err
	}
	if err = d.Set("run_on_startup", entry.Content.RunOnStartup); err != nil {
		return err
	}
	if err = d.Set("schedule_window", entry.Content.ScheduleWindow); err != nil {
		return err
	}
	if err = d.Set("schedule_priority", entry.Content.SchedulePriority); err != nil {
		return err
	}
	if err = d.Set("search", entry.Content.Search); err != nil {
		return err
	}
	if err = d.Set("vsid", entry.Content.VSID); err != nil {
		return err
	}
	if err = d.Set("workload_pool", entry.Content.WorkloadPool); err != nil {
		return err
	}

	err = d.Set("acl", flattenACL(&entry.ACL))
	if err != nil {
		return err
	}

	return nil
}

func savedSearchesUpdate(d *schema.ResourceData, meta interface{}) error {
	provider := meta.(*SplunkProvider)
	savedSearchesConfig := getSavedSearchesConfig(d)
	aclObject := getACLConfig(d.Get("acl").([]interface{}))

	// Update will create a new resource with private `user` permissions if resource had shared permissions set
	var owner string
	if aclObject.Sharing != "user" {
		owner = "nobody"
	} else {
		owner = aclObject.Owner
	}

	err := (*provider.Client).UpdateSavedSearches(d.Id(), owner, aclObject.App, savedSearchesConfig)
	if err != nil {
		return err
	}

	// Update ACL
	err = (*provider.Client).UpdateAcl(owner, aclObject.App, d.Id(), aclObject, "saved", "searches")
	if err != nil {
		return err
	}

	return savedSearchesRead(d, meta)
}

func savedSearchesDelete(d *schema.ResourceData, meta interface{}) error {
	provider := meta.(*SplunkProvider)
	aclObject := getACLConfig(d.Get("acl").([]interface{}))
	resp, err := (*provider.Client).DeleteSavedSearches(d.Id(), aclObject.Owner, aclObject.App)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case 200, 201:
		return nil

	default:
		errorResponse := &models.InputsUDPResponse{}
		_ = json.NewDecoder(resp.Body).Decode(errorResponse)
		err := errors.New(errorResponse.Messages[0].Text)
		return err
	}
}

func getSavedSearchesConfig(d *schema.ResourceData) (savedSearchesObj *models.SavedSearchObject) {
	savedSearchesObj = &models.SavedSearchObject{
		Actions:                                      normalizeActionsString(d.Get("actions").(string)),
		ActionEmail:                                  d.Get("action_email").(bool),
		ActionEmailAuthPassword:                      d.Get("action_email_auth_password").(string),
		ActionEmailAuthUsername:                      d.Get("action_email_auth_username").(string),
		ActionEmailBCC:                               d.Get("action_email_bcc").(string),
		ActionEmailCC:                                d.Get("action_email_cc").(string),
		ActionEmailFormat:                            d.Get("action_email_format").(string),
		ActionEmailFrom:                              d.Get("action_email_from").(string),
		ActionEmailHostname:                          d.Get("action_email_hostname").(string),
		ActionEmailIncludeResultsLink:                d.Get("action_email_include_results_link").(int),
		ActionEmailIncludeSearch:                     d.Get("action_email_include_search").(int),
		ActionEmailIncludeTrigger:                    d.Get("action_email_include_trigger").(int),
		ActionEmailIncludeTriggerTime:                d.Get("action_email_include_trigger_time").(int),
		ActionEmailIncludeViewLink:                   d.Get("action_email_include_view_link").(int),
		ActionEmailInline:                            d.Get("action_email_inline").(bool),
		ActionEmailMailserver:                        d.Get("action_email_mailserver").(string),
		ActionEmailMaxResults:                        d.Get("action_email_max_results").(int),
		ActionEmailMaxTime:                           d.Get("action_email_max_time").(string),
		ActionEmailMessageAlert:                      d.Get("action_email_message_alert").(string),
		ActionEmailMessageReport:                     d.Get("action_email_message_report").(string),
		ActionEmailPDFView:                           d.Get("action_email_pdfview").(string),
		ActionEmailPreprocessResults:                 d.Get("action_email_preprocess_results").(string),
		ActionEmailReportCIDFontList:                 d.Get("action_email_report_cid_font_list").(string),
		ActionEmailReportIncludeSplunkLogo:           d.Get("action_email_report_include_splunk_logo").(bool),
		ActionEmailReportPaperOrientation:            d.Get("action_email_report_paper_orientation").(string),
		ActionEmailReportPaperSize:                   d.Get("action_email_report_paper_size").(string),
		ActionEmailReportServerEnabled:               d.Get("action_email_report_server_enabled").(bool),
		ActionEmailReportServerURL:                   d.Get("action_email_report_server_url").(string),
		ActionEmailSendCSV:                           d.Get("action_email_send_csv").(int),
		ActionEmailSendPDF:                           d.Get("action_email_send_pdf").(bool),
		ActionEmailSendResults:                       d.Get("action_email_send_results").(bool),
		ActionEmailAllowEmptyAttachment:              d.Get("action_email_allow_empty_attachment").(int),
		ActionEmailSubject:                           d.Get("action_email_subject").(string),
		ActionEmailTo:                                d.Get("action_email_to").(string),
		ActionEmailTrackAlert:                        d.Get("action_email_track_alert").(bool),
		ActionEmailTTL:                               d.Get("action_email_ttl").(string),
		ActionEmailUseSSL:                            d.Get("action_email_use_ssl").(bool),
		ActionEmailUseTLS:                            d.Get("action_email_use_tls").(bool),
		ActionEmailWidthSortColumns:                  d.Get("action_email_width_sort_columns").(bool),
		ActionPagerdutyIntegrationURL:                d.Get("action_pagerduty_integration_url").(string),
		ActionPagerdutyIntegrationURLOverride:        d.Get("action_pagerduty_integration_url_override").(string),
		ActionPagerdutyParamCustDetails:              d.Get("action_pagerduty_custom_details").(string),
		ActionPagerdutyParamIntKey:                   d.Get("action_pagerduty_integration_key").(string),
		ActionPagerdutyParamIntKeyOverride:           d.Get("action_pagerduty_integration_key_override").(string),
		ActionScriptFilename:                         d.Get("action_script_filename").(string),
		ActionSnowEventParamAccount:                  d.Get("action_snow_event_param_account").(string),
		ActionSnowEventParamNode:                     d.Get("action_snow_event_param_node").(string),
		ActionSnowEventParamType:                     d.Get("action_snow_event_param_type").(string),
		ActionSnowEventParamResource:                 d.Get("action_snow_event_param_resource").(string),
		ActionSnowEventParamSeverity:                 d.Get("action_snow_event_param_severity").(int),
		ActionSnowEventParamDescription:              d.Get("action_snow_event_param_description").(string),
		ActionSnowEventParamCiIdentifier:             d.Get("action_snow_event_param_ci_identifier").(string),
		ActionSnowEventParamCustomFields:             d.Get("action_snow_event_param_custom_fields").(string),
		ActionSnowEventParamAdditionalInfo:           d.Get("action_snow_event_param_additional_info").(string),
		ActionCreateXsoarIncident:                    d.Get("action_create_xsoar_incident").(string),
		ActionCreateXsoarIncidentParamSendAllServers: d.Get("action_create_xsoar_incident_param_send_all_servers").(string),
		ActionCreateXsoarIncidentParamServerUrl:      d.Get("action_create_xsoar_incident_param_server_url").(string),
		ActionCreateXsoarIncidentParamIncidentName:   d.Get("action_create_xsoar_incident_param_incident_name").(string),
		ActionCreateXsoarIncidentParamDetails:        d.Get("action_create_xsoar_incident_param_details").(string),
		ActionCreateXsoarIncidentParamCustomFields:   d.Get("action_create_xsoar_incident_param_custom_fields").(string),
		ActionCreateXsoarIncidentParamSeverity:       d.Get("action_create_xsoar_incident_param_severity").(string),
		ActionCreateXsoarIncidentParamOccurred:       d.Get("action_create_xsoar_incident_param_occurred").(string),
		ActionCreateXsoarIncidentParamType:           d.Get("action_create_xsoar_incident_param_type").(string),
		ActionSlackParamAttachment:                   d.Get("action_slack_param_attachment").(string),
		ActionSlackParamChannel:                      d.Get("action_slack_param_channel").(string),
		ActionSlackParamFields:                       d.Get("action_slack_param_fields").(string),
		ActionSlackParamMessage:                      d.Get("action_slack_param_message").(string),
		ActionSlackParamWebhookUrlOverride:           d.Get("action_slack_param_webhook_url_override").(string),
		ActionJiraServiceDeskParamAccount:            d.Get("action_jira_service_desk_param_account").(string),
		ActionJiraServiceDeskParamJiraProject:        d.Get("action_jira_service_desk_param_jira_project").(string),
		ActionJiraServiceDeskParamJiraIssueType:      d.Get("action_jira_service_desk_param_jira_issue_type").(string),
		ActionJiraServiceDeskParamJiraSummary:        d.Get("action_jira_service_desk_param_jira_summary").(string),
		ActionJiraServiceDeskParamJiraPriority:       d.Get("action_jira_service_desk_param_jira_priority").(string),
		ActionJiraServiceDeskParamJiraDescription:    d.Get("action_jira_service_desk_param_jira_description").(string),
		ActionJiraServiceDeskParamJiraCustomfields:   d.Get("action_jira_service_desk_param_jira_customfields").(string),
		ActionWebhookParamUrl:                        d.Get("action_webhook_param_url").(string),
		ActionWebhook:                                strings.Contains(normalizeActionsString(d.Get("actions").(string)), "webhook"),
		ActionWebhookEnableAllowlist:                 d.Get("action_webhook_enable_allowlist").(int),
		ActionWebhookParamPriority:                   getCalculatedPriority(d),
		ActionWebhookParamMitreAttackId:              d.Get("action_webhook_param_mitre_attack_id").(string),
		ActionWebhookParamDescription:                d.Get("action_webhook_param_description").(string),
		ActionWebhookParamFields:                     d.Get("action_webhook_param_fields").(string),
		ActionWebhookParamTags:                       d.Get("action_webhook_param_tags").(string),
		ActionWebhookParamAuthor:                     d.Get("action_webhook_param_author").(string),
		AlertComparator:                              d.Get("alert_comparator").(string),
		AlertCondition:                               d.Get("alert_condition").(string),
		AlertDigestMode:                              d.Get("alert_digest_mode").(bool),
		AlertExpires:                                 d.Get("alert_expires").(string),
		AlertSeverity:                                d.Get("alert_severity").(int),
		AlertSuppress:                                d.Get("alert_suppress").(bool),
		AlertSuppressFields:                          d.Get("alert_suppress_fields").(string),
		AlertSuppressPeriod:                          d.Get("alert_suppress_period").(string),
		AlertThreshold:                               d.Get("alert_threshold").(string),
		AlertTrack:                                   d.Get("alert_track").(bool),
		AlertType:                                    d.Get("alert_type").(string),
		AutoSummarize:                                d.Get("auto_summarize").(bool),
		AutoSummarizeCommand:                         d.Get("auto_summarize_command").(string),
		AutoSummarizeCronSchedule:                    d.Get("auto_summarize_cron_schedule").(string),
		AutoSummarizeDispatchEarliestTime:            d.Get("auto_summarize_dispatch_earliest_time").(string),
		AutoSummarizeDispatchLatestTime:              d.Get("auto_summarize_dispatch_latest_time").(string),
		AutoSummarizeDispatchTimeFormat:              d.Get("auto_summarize_dispatch_time_format").(string),
		AutoSummarizeDispatchTTL:                     d.Get("auto_summarize_dispatch_ttl").(string),
		AutoSummarizeMaxDisabledBuckets:              d.Get("auto_summarize_max_disabled_buckets").(int),
		AutoSummarizeMaxSummaryRatio:                 d.Get("auto_summarize_max_summary_ratio").(float64),
		AutoSummarizeMaxSummarySize:                  d.Get("auto_summarize_max_summary_size").(int),
		AutoSummarizeMaxTime:                         d.Get("auto_summarize_max_time").(int),
		AutoSummarizeSuspendPeriod:                   d.Get("auto_summarize_suspend_period").(string),
		AutoSummarizeTimespan:                        d.Get("auto_summarize_timespan").(string),
		CronSchedule:                                 d.Get("cron_schedule").(string),
		Description:                                  d.Get("description").(string),
		Disabled:                                     d.Get("disabled").(bool),
		DispatchBuckets:                              d.Get("dispatch_buckets").(int),
		DispatchEarliestTime:                         d.Get("dispatch_earliest_time").(string),
		DispatchIndexEarliest:                        d.Get("dispatch_index_earliest").(string),
		DispatchIndexLatest:                          d.Get("dispatch_index_latest").(string),
		DispatchIndexedRealtime:                      d.Get("dispatch_indexed_realtime").(bool),
		DispatchIndexedRealtimeOffset:                d.Get("dispatch_indexed_realtime_offset").(int),
		DispatchIndexedRealtimeMinspan:               d.Get("dispatch_indexed_realtime_minspan").(int),
		DispatchLatestTime:                           d.Get("dispatch_latest_time").(string),
		DispatchLookups:                              d.Get("dispatch_lookups").(bool),
		DispatchMaxCount:                             d.Get("dispatch_max_count").(int),
		DispatchMaxTime:                              d.Get("dispatch_max_time").(int),
		DispatchReduceFreq:                           d.Get("dispatch_reduce_freq").(int),
		DispatchRtBackfill:                           d.Get("dispatch_rt_backfill").(bool),
		DispatchRtMaximumSpan:                        d.Get("dispatch_rt_maximum_span").(int),
		DispatchSpawnProcess:                         d.Get("dispatch_spawn_process").(bool),
		DispatchTimeFormat:                           d.Get("dispatch_time_format").(string),
		DispatchTTL:                                  d.Get("dispatch_ttl").(string),
		DisplayView:                                  d.Get("display_view").(string),
		IsScheduled:                                  d.Get("is_scheduled").(bool),
		IsVisible:                                    d.Get("is_visible").(bool),
		MaxConcurrent:                                d.Get("max_concurrent").(int),
		RealtimeSchedule:                             d.Get("realtime_schedule").(bool),
		RequestUIDispatchApp:                         d.Get("request_ui_dispatch_app").(string),
		RequestUIDispatchView:                        d.Get("request_ui_dispatch_view").(string),
		RestartOnSearchPeerAdd:                       d.Get("restart_on_searchpeer_add").(bool),
		RunOnStartup:                                 d.Get("run_on_startup").(bool),
		ScheduleWindow:                               d.Get("schedule_window").(string),
		SchedulePriority:                             d.Get("schedule_priority").(string),
		Search:                                       d.Get("search").(string),
		VSID:                                         d.Get("vsid").(string),
		WorkloadPool:                                 d.Get("workload_pool").(string),
	}
	return savedSearchesObj
}

func getSavedSearchesConfigByName(name string, httpResponse *http.Response) (savedSearchesEntry *models.SavedSearchesEntry, err error) {
	response := &models.SavedSearchesResponse{}
	switch httpResponse.StatusCode {
	case 200, 201:
		_ = json.NewDecoder(httpResponse.Body).Decode(&response)
		re := regexp.MustCompile(`(.*)`)
		for _, entry := range response.Entry {
			if name == re.FindStringSubmatch(entry.Name)[1] {
				return &entry, nil
			}
		}

	default:
		_ = json.NewDecoder(httpResponse.Body).Decode(response)
		err := errors.New(response.Messages[0].Text)
		return savedSearchesEntry, err
	}

	return savedSearchesEntry, nil
}

// getResourceDataSearchACL implements psuedo-defaults for the acl field for search resources.
func getResourceDataSearchACL(d *schema.ResourceData) *models.ACLObject {
	aclObject := &models.ACLObject{}
	if r, ok := d.GetOk("acl"); ok {
		aclObject = getACLConfig(r.([]interface{}))
	} else {
		aclObject.App = "search"
		aclObject.Owner = "nobody"
	}

	return aclObject
}
