// Copyright (c) 2017-present Mattermost, Inc. All Rights Reserved.
// See LICENSE.txt for license information.

package main

import (
	"crypto/rand"
	"crypto/rsa"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	jira "github.com/andygrunwald/go-jira"
	"github.com/jarcoal/httpmock"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-plugin-jira/server/enterprise"
	"github.com/mattermost/mattermost-plugin-jira/server/utils/types"
)

const (
	mockUserIDWithNotifications    = "1"
	mockUserIDWithoutNotifications = "2"
	mockUserIDUnknown              = "3"
	mockUserIDSysAdmin             = "4"
	mockUserIDNonSysAdmin          = "5"
	mattermostSiteURL              = "https://somelink.com"
)

type mockUserStoreKV struct {
	mockUserStore
	connections map[types.ID]*Connection
	users       map[types.ID]*User
}

var _ UserStore = (*mockUserStoreKV)(nil)

func (store mockUserStoreKV) LoadConnection(instanceID, mattermostUserID types.ID) (*Connection, error) {
	connection, ok := store.connections[mattermostUserID]
	if !ok {
		return nil, errors.Errorf("TESTING connection %q %q not found", instanceID, mattermostUserID)
	}
	return connection, nil
}

func (store mockUserStoreKV) LoadUser(mattermostUserID types.ID) (*User, error) {
	user, ok := store.users[mattermostUserID]
	if !ok {
		return nil, errors.Errorf("TESTING user %q not found", mattermostUserID)
	}
	return user, nil
}

func getMockUserStoreKV() mockUserStoreKV {
	newuser := func(id types.ID) *User {
		u := NewUser(id)
		u.ConnectedInstances.Set(testInstance1.Common())
		return u
	}

	connection := Connection{
		User: jira.User{
			AccountID: "test",
		},
	}

	withNotifications := connection // copy
	withNotifications.Settings = &ConnectionSettings{Notifications: true}

	return mockUserStoreKV{
		users: map[types.ID]*User{
			"connected_user":               newuser("connected_user"),
			mockUserIDWithNotifications:    newuser(mockUserIDWithNotifications),
			mockUserIDWithoutNotifications: newuser(mockUserIDWithoutNotifications),
		},
		connections: map[types.ID]*Connection{
			mockUserIDWithNotifications:    &withNotifications,
			mockUserIDWithoutNotifications: &connection,
			"connected_user":               &connection,
		},
	}
}

type mockInstanceStoreKV struct {
	mockInstanceStore
	kv *sync.Map
	*Instances
	*Plugin
}

var _ InstanceStore = (*mockInstanceStoreKV)(nil)

func (store *mockInstanceStoreKV) LoadInstances() (*Instances, error) {
	return store.Instances, nil
}

func (store *mockInstanceStoreKV) LoadInstance(id types.ID) (Instance, error) {
	v, ok := store.kv.Load(id)
	if !ok {
		return nil, errors.Errorf("instance %q not found", id)
	}
	instance := v.(Instance)
	return instance, nil
}

func (p *Plugin) getMockInstanceStoreKV(n int) *mockInstanceStoreKV {
	kv := sync.Map{}
	instances := NewInstances()

	if n > 2 || n == 0 {
		return &mockInstanceStoreKV{
			kv:        &kv,
			Instances: instances,
			Plugin:    p,
		}
	}

	for i, ti := range []*testInstance{testInstance1, testInstance2} {
		if i > n {
			break
		}
		instance := *ti
		p.client = pluginapi.NewClient(p.API, p.Driver)
		instance.Plugin = p
		instances.Set(instance.Common())

		kv.Store(instance.GetID(), &instance)
	}

	return &mockInstanceStoreKV{
		kv:        &kv,
		Instances: instances,
		Plugin:    p,
	}
}

func TestPlugin_ExecuteCommand_Settings(t *testing.T) {
	p := &Plugin{}
	tc := TestConfiguration{}
	p.updateConfig(func(conf *config) {
		conf.Secret = tc.Secret
		conf.mattermostSiteURL = mattermostSiteURL
	})
	api := &plugintest.API{}
	api.On("LogError", mock.AnythingOfType("string")).Return(nil)

	tests := map[string]struct {
		commandArgs  *model.CommandArgs
		numInstances int
		expectedMsg  string
	}{
		"no storage": {
			commandArgs:  &model.CommandArgs{Command: "/jira settings", UserId: mockUserIDUnknown},
			numInstances: 2,
			expectedMsg:  "Failed to load your connection to Jira. Error: TESTING user \"3\" not found.",
		},
		"user not found": {
			commandArgs:  &model.CommandArgs{Command: "/jira settings", UserId: mockUserIDUnknown},
			numInstances: 0,
			expectedMsg:  "Failed to load your connection to Jira. Error: TESTING user \"3\" not found.",
		},
		"no params, with notifications": {
			commandArgs:  &model.CommandArgs{Command: "/jira settings", UserId: mockUserIDWithNotifications},
			numInstances: 1,
			expectedMsg:  "Current settings:\n\tNotifications: on",
		},
		"no params, without notifications": {
			commandArgs:  &model.CommandArgs{Command: "/jira settings", UserId: mockUserIDWithoutNotifications},
			numInstances: 1,
			expectedMsg:  "Current settings:\n\tNotifications: off",
		},
		"unknown setting": {
			commandArgs:  &model.CommandArgs{Command: "/jira settings" + " test", UserId: mockUserIDWithoutNotifications},
			numInstances: 1,
			expectedMsg:  "Unknown setting.",
		},
		"set notifications without value": {
			commandArgs:  &model.CommandArgs{Command: "/jira settings" + " notifications", UserId: mockUserIDWithoutNotifications},
			numInstances: 1,
			expectedMsg:  "`/jira settings notifications [value]`\n* Invalid value. Accepted values are: `on` or `off`.",
		},
		"set notification with unknown value": {
			commandArgs:  &model.CommandArgs{Command: "/jira settings notifications test", UserId: mockUserIDWithoutNotifications},
			numInstances: 1,
			expectedMsg:  "`/jira settings notifications [value]`\n* Invalid value. Accepted values are: `on` or `off`.",
		},
		"enable notifications": {
			commandArgs:  &model.CommandArgs{Command: "/jira settings notifications on", UserId: mockUserIDWithoutNotifications},
			numInstances: 1,
			expectedMsg:  "Settings updated. Notifications on.",
		},
		"disable notifications": {
			commandArgs:  &model.CommandArgs{Command: "/jira settings notifications off", UserId: mockUserIDWithNotifications},
			numInstances: 1,
			expectedMsg:  "Settings updated. Notifications off.",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			isSendEphemeralPostCalled := false

			currentTestAPI := api
			currentTestAPI.On("SendEphemeralPost", mock.AnythingOfType("string"), mock.AnythingOfType("*model.Post")).Run(func(args mock.Arguments) {
				isSendEphemeralPostCalled = true

				post := args.Get(1).(*model.Post)
				assert.Equal(t, tt.expectedMsg, post.Message)
			}).Once().Return(&model.Post{})

			p.SetAPI(currentTestAPI)
			p.client = pluginapi.NewClient(p.API, p.Driver)
			p.instanceStore = p.getMockInstanceStoreKV(tt.numInstances)
			p.userStore = getMockUserStoreKV()

			_, err := p.ExecuteCommand(&plugin.Context{}, tt.commandArgs)
			require.Nil(t, err)

			assert.Equal(t, true, isSendEphemeralPostCalled)
		})
	}
}

func TestPlugin_ExecuteCommand_Instance_Settings(t *testing.T) {
	p := &Plugin{}
	tc := TestConfiguration{}
	p.updateConfig(func(conf *config) {
		conf.Secret = tc.Secret
		conf.mattermostSiteURL = mattermostSiteURL
	})
	api := &plugintest.API{}
	api.On("LogError", mock.AnythingOfType("string")).Return(nil)

	tests := map[string]struct {
		commandArgs  *model.CommandArgs
		numInstances int
		expectedMsg  string
	}{
		"no storage": {
			commandArgs:  &model.CommandArgs{Command: "/jira instance settings", UserId: mockUserIDUnknown},
			numInstances: 2,
			expectedMsg:  "Failed to load your connection to Jira. Error: TESTING user \"3\" not found.",
		},
		"user not found": {
			commandArgs:  &model.CommandArgs{Command: "/jira instance settings", UserId: mockUserIDUnknown},
			numInstances: 0,
			expectedMsg:  "Failed to load your connection to Jira. Error: TESTING user \"3\" not found.",
		},
		"no params, with notifications": {
			commandArgs:  &model.CommandArgs{Command: "/jira instance settings", UserId: mockUserIDWithNotifications},
			numInstances: 1,
			expectedMsg:  "Current settings:\n\tNotifications: on",
		},
		"no params, without notifications": {
			commandArgs:  &model.CommandArgs{Command: "/jira instance settings", UserId: mockUserIDWithoutNotifications},
			numInstances: 1,
			expectedMsg:  "Current settings:\n\tNotifications: off",
		},
		"unknown setting": {
			commandArgs:  &model.CommandArgs{Command: "/jira instance settings" + " test", UserId: mockUserIDWithoutNotifications},
			numInstances: 1,
			expectedMsg:  "Unknown setting.",
		},
		"set notifications without value": {
			commandArgs:  &model.CommandArgs{Command: "/jira instance settings" + " notifications", UserId: mockUserIDWithoutNotifications},
			numInstances: 1,
			expectedMsg:  "`/jira settings notifications [value]`\n* Invalid value. Accepted values are: `on` or `off`.",
		},
		"set notification with unknown value": {
			commandArgs:  &model.CommandArgs{Command: "/jira instance settings notifications test", UserId: mockUserIDWithoutNotifications},
			numInstances: 1,
			expectedMsg:  "`/jira settings notifications [value]`\n* Invalid value. Accepted values are: `on` or `off`.",
		},
		"enable notifications": {
			commandArgs:  &model.CommandArgs{Command: "/jira instance settings notifications on", UserId: mockUserIDWithoutNotifications},
			numInstances: 1,
			expectedMsg:  "Settings updated. Notifications on.",
		},
		"disable notifications": {
			commandArgs:  &model.CommandArgs{Command: "/jira instance settings notifications off", UserId: mockUserIDWithNotifications},
			numInstances: 1,
			expectedMsg:  "Settings updated. Notifications off.",
		},
		"multiple instances are present: Notifications off": {
			commandArgs:  &model.CommandArgs{Command: "/jira instance settings notifications off --instance https://jiraurl1.com", UserId: mockUserIDWithNotifications},
			numInstances: 2,
			expectedMsg:  "Settings updated. Notifications off.",
		},
		"multiple instances are present: Notifications on": {
			commandArgs:  &model.CommandArgs{Command: "/jira instance settings notifications on --instance https://jiraurl2.com", UserId: mockUserIDWithNotifications},
			numInstances: 2,
			expectedMsg:  "Settings updated. Notifications on.",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			isSendEphemeralPostCalled := false

			currentTestAPI := api
			currentTestAPI.On("SendEphemeralPost", mock.AnythingOfType("string"), mock.AnythingOfType("*model.Post")).Run(func(args mock.Arguments) {
				isSendEphemeralPostCalled = true

				post := args.Get(1).(*model.Post)
				assert.Equal(t, tt.expectedMsg, post.Message)
			}).Once().Return(&model.Post{})

			p.SetAPI(currentTestAPI)
			p.client = pluginapi.NewClient(p.API, p.Driver)
			p.instanceStore = p.getMockInstanceStoreKV(tt.numInstances)
			p.userStore = getMockUserStoreKV()

			_, err := p.ExecuteCommand(&plugin.Context{}, tt.commandArgs)
			require.Nil(t, err)

			assert.Equal(t, true, isSendEphemeralPostCalled)
		})
	}
}

func TestPlugin_ExecuteCommand_Installation(t *testing.T) {
	api := &plugintest.API{}
	api.On("LogError", mock.AnythingOfType("string")).Return(nil)
	api.On("LogDebug", mockAnythingOfTypeBatch("string", 11)...).Return(nil)
	api.On("KVSet", mock.AnythingOfType("string"), mock.Anything, mock.Anything).Return(nil)
	api.On("KVSetWithExpiry", mock.AnythingOfType("string"), mock.Anything, mock.Anything).Return(nil)
	api.On("KVGet", keyInstances).Return(nil, nil)
	api.On("KVGet", "rsa_key").Return(nil, nil)
	api.On("PublishWebSocketEvent", mock.AnythingOfType("string"), mock.Anything, mock.Anything)
	api.On("UnregisterCommand", mock.AnythingOfType("string"), mock.AnythingOfType("string")).Return(nil)

	sysAdminUser := &model.User{
		Id:    mockUserIDSysAdmin,
		Roles: "system_admin",
	}
	api.On("GetUser", mockUserIDSysAdmin).Return(sysAdminUser, nil)
	nonSysAdminUser := &model.User{
		Id:    mockUserIDNonSysAdmin,
		Roles: "",
	}
	api.On("GetUser", mockUserIDNonSysAdmin).Return(nonSysAdminUser, nil)

	httpmock.Activate()
	defer httpmock.DeactivateAndReset()

	httpmock.RegisterResponder("GET", "https://jiralink.com/status",
		httpmock.NewStringResponder(200, `{"state": "RUNNING"}`))
	httpmock.RegisterResponder("GET", "https://mmtest.atlassian.net/status",
		httpmock.NewStringResponder(200, `{"state": "RUNNING"}`))
	httpmock.RegisterResponder("GET", "http://mmtest.atlassian.net/status",
		httpmock.NewStringResponder(200, `{"state": "RUNNING"}`))
	httpmock.RegisterResponder("GET", "https://non-existing-jira-page.atlassian.net/status",
		httpmock.NewStringResponder(404, ``))
	httpmock.RegisterResponder("GET", "https://valid-jira-page.atlassian.net/status",
		httpmock.NewStringResponder(200, ``))
	httpmock.RegisterResponder("GET", "https://inaccessible-jira-server.com/status",
		httpmock.NewStringResponder(404, ``))
	httpmock.RegisterResponder("GET", "https://starting-jira-server.com/status",
		httpmock.NewStringResponder(200, `{"state": "STARTING"}`))
	httpmock.RegisterResponder("GET", "https://broken-json-response.com/status",
		httpmock.NewStringResponder(200, `{"invalid-json": }`))

	tests := map[string]struct {
		commandArgs       *model.CommandArgs
		numInstances      int
		expectedMsgPrefix string
	}{
		"no params - user is sys admin": {
			commandArgs:       &model.CommandArgs{Command: "/jira install", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: strings.TrimSpace(helpTextHeader + commonHelpText + sysAdminHelpText),
		},
		"no params - user is not sys admin": {
			commandArgs:       &model.CommandArgs{Command: "/jira install", UserId: mockUserIDNonSysAdmin},
			expectedMsgPrefix: strings.TrimSpace(helpTextHeader + commonHelpText),
		},
		"install server without URL": {
			commandArgs:       &model.CommandArgs{Command: "/jira install server", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: strings.TrimSpace(helpTextHeader + commonHelpText + sysAdminHelpText),
		},
		"install cloud instance without URL": {
			commandArgs:       &model.CommandArgs{Command: "/jira install cloud", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: strings.TrimSpace(helpTextHeader + commonHelpText + sysAdminHelpText),
		},
		"install cloud instance as server": {
			commandArgs:       &model.CommandArgs{Command: "/jira install server https://mmtest.atlassian.net", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: "`https://mmtest.atlassian.net` is not a Jira server URL, it refers to Jira Cloud",
		},
		"install server instance using mattermost site URL": {
			commandArgs:       &model.CommandArgs{Command: "/jira install server https://somelink.com", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: "https://somelink.com is the Mattermost site URL. Please use your Jira URL",
		},
		"install valid cloud instance": {
			numInstances:      0,
			commandArgs:       &model.CommandArgs{Command: "/jira install cloud https://mmtest.atlassian.net", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: "https://mmtest.atlassian.net has been successfully added.",
		},
		"install inaccessible cloud instance": {
			numInstances:      0,
			commandArgs:       &model.CommandArgs{Command: "/jira install cloud https://non-existing-jira-page.atlassian.net", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: `we couldn't validate the connection to your Jira server. This could be because of existing firewall or proxy rules, or because the URL was entered incorrectly: Jira server returned http status code "404" when checking for availability: "https://non-existing-jira-page.atlassian.net"`,
		},
		"install valid cloud instance with broken json response": {
			numInstances:      0,
			commandArgs:       &model.CommandArgs{Command: "/jira install cloud https://broken-json-response.com", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: "we couldn't validate the connection to your Jira server. This could be because of existing firewall or proxy rules, or because the URL was entered incorrectly: invalid character '}' looking for beginning of value",
		},
		"install valid server instance 1 preinstalled": {
			numInstances:      1,
			commandArgs:       &model.CommandArgs{Command: "/jira install server https://jiralink.com", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: "https://jiralink.com has been successfully added",
		},
		"install valid server instance 2 preinstalled": {
			numInstances:      2,
			commandArgs:       &model.CommandArgs{Command: "/jira install server https://jiralink.com", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: "https://jiralink.com has been successfully added",
		},
		"install inaccessible server instance": {
			numInstances:      0,
			commandArgs:       &model.CommandArgs{Command: "/jira install server https://inaccessible-jira-server.com", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: `we couldn't validate the connection to your Jira server. This could be because of existing firewall or proxy rules, or because the URL was entered incorrectly: Jira server returned http status code "404" when checking for availability: "https://inaccessible-jira-server.com"`,
		},
		"install valid server instance in wrong state": {
			numInstances:      0,
			commandArgs:       &model.CommandArgs{Command: "/jira install server https://starting-jira-server.com", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: `we couldn't validate the connection to your Jira server. This could be because of existing firewall or proxy rules, or because the URL was entered incorrectly: Jira server is not in correct state, it should be up and running: "https://starting-jira-server.com"`,
		},
		"install valid server instance with broken json response": {
			numInstances:      0,
			commandArgs:       &model.CommandArgs{Command: "/jira install server https://broken-json-response.com", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: "we couldn't validate the connection to your Jira server. This could be because of existing firewall or proxy rules, or because the URL was entered incorrectly: invalid character '}' looking for beginning of value",
		},
		"install non secure cloud instance": {
			commandArgs:       &model.CommandArgs{Command: "/jira install cloud http://mmtest.atlassian.net", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: "a secure HTTPS URL is required",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			p := Plugin{}
			p.updateConfig(func(conf *config) {
				conf.mattermostSiteURL = mattermostSiteURL
				conf.rsaKey, _ = rsa.GenerateKey(rand.Reader, 1024) // #nosec G403
			})
			isSendEphemeralPostCalled := false

			// add valid license
			var license model.License
			license.SkuShortName = "professional"

			api.On("GetLicense").Return(&license)
			api.On("GetConfig").Return(&model.Config{})
			api.On("RegisterCommand", mock.Anything).Return(nil)
			api.On("GetBundlePath").Return("", nil)
			api.On("SendEphemeralPost", mock.AnythingOfType("string"), mock.AnythingOfType("*model.Post")).Run(func(args mock.Arguments) {
				isSendEphemeralPostCalled = true

				post := args.Get(1).(*model.Post)
				actual := strings.TrimSpace(post.Message)
				assert.True(t, strings.HasPrefix(actual, tt.expectedMsgPrefix), "Expected returned message to start with: \n%s\nActual:\n%s", tt.expectedMsgPrefix, actual)
			}).Once().Return(&model.Post{})

			path, err := filepath.Abs("..")
			require.Nil(t, err)
			api.On("GetBundlePath").Return(path, nil)

			p.SetAPI(api)
			p.client = pluginapi.NewClient(p.API, p.Driver)
			_, filename, _, _ := runtime.Caller(0)
			htmlTemplates, textTemplates, err := p.loadTemplates(filepath.Dir(filename) + "/../assets/templates")
			require.NoError(t, err)
			p.htmlTemplates = htmlTemplates
			p.textTemplates = textTemplates

			store := NewStore(&p)
			p.instanceStore = p.getMockInstanceStoreKV(tt.numInstances)
			p.secretsStore = store
			p.userStore = getMockUserStoreKV()
			p.enterpriseChecker = enterprise.NewEnterpriseChecker(api)

			cmdResponse, err := p.ExecuteCommand(&plugin.Context{}, tt.commandArgs)
			require.Nil(t, err)
			require.NotNil(t, cmdResponse)
			assert.True(t, isSendEphemeralPostCalled)
		})
	}
}

func TestPlugin_ExecuteCommand_Uninstall(t *testing.T) {
	api := &plugintest.API{}

	sysAdminUser := &model.User{
		Id:    mockUserIDSysAdmin,
		Roles: "system_admin",
	}
	api.On("GetUser", mockUserIDSysAdmin).Return(sysAdminUser, nil)
	nonSysAdminUser := &model.User{
		Id:    mockUserIDNonSysAdmin,
		Roles: "",
	}
	api.On("GetUser", mockUserIDNonSysAdmin).Return(nonSysAdminUser, nil)

	tests := map[string]struct {
		commandArgs       *model.CommandArgs
		expectedMsgPrefix string
	}{
		"no params - user is sys admin": {
			commandArgs:       &model.CommandArgs{Command: "/jira uninstall", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: strings.TrimSpace(helpTextHeader + commonHelpText + sysAdminHelpText),
		},
		"no params - user is not sys admin": {
			commandArgs:       &model.CommandArgs{Command: "/jira uninstall", UserId: mockUserIDNonSysAdmin},
			expectedMsgPrefix: "`/jira uninstall` can only be run by a System Administrator.",
		},
		"uninstall with invalid option": {
			commandArgs:       &model.CommandArgs{Command: "/jira uninstall foo", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: strings.TrimSpace(helpTextHeader + commonHelpText + sysAdminHelpText),
		},
		"uninstall server instance without URL": {
			commandArgs:       &model.CommandArgs{Command: "/jira uninstall server", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: strings.TrimSpace(helpTextHeader + commonHelpText + sysAdminHelpText),
		},
		"uninstall cloud instance without URL": {
			commandArgs:       &model.CommandArgs{Command: "/jira uninstall cloud", UserId: mockUserIDSysAdmin},
			expectedMsgPrefix: strings.TrimSpace(helpTextHeader + commonHelpText + sysAdminHelpText),
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			p := Plugin{}
			p.updateConfig(func(conf *config) {
				conf.mattermostSiteURL = mattermostSiteURL
			})
			isSendEphemeralPostCalled := false
			currentTestAPI := api
			currentTestAPI.On("SendEphemeralPost", mock.AnythingOfType("string"), mock.AnythingOfType("*model.Post")).Run(func(args mock.Arguments) {
				isSendEphemeralPostCalled = true

				post := args.Get(1).(*model.Post)
				actual := strings.TrimSpace(post.Message)
				assert.True(t, strings.HasPrefix(actual, tt.expectedMsgPrefix), "Expected returned message to start with: \n%s\nActual:\n%s", tt.expectedMsgPrefix, actual)
			}).Once().Return(&model.Post{})

			p.SetAPI(currentTestAPI)
			p.client = pluginapi.NewClient(p.API, p.Driver)

			cmdResponse, err := p.ExecuteCommand(&plugin.Context{}, tt.commandArgs)
			require.Nil(t, err)
			require.NotNil(t, cmdResponse)
			assert.True(t, isSendEphemeralPostCalled)
		})
	}
}

func TestPlugin_ExecuteCommand_Assign(t *testing.T) {
	p := &Plugin{}
	tc := TestConfiguration{}
	p.updateConfig(func(conf *config) {
		conf.Secret = tc.Secret
		conf.mattermostSiteURL = mattermostSiteURL
	})

	tests := map[string]struct {
		commandArgs       *model.CommandArgs
		expectedMsgPrefix string
		SetupAPI          func(api *plugintest.API)
	}{
		"assign with no issue": {
			commandArgs: &model.CommandArgs{
				Command: "/jira assign",
				UserId:  mockUserIDWithNotifications,
			},
			expectedMsgPrefix: "Please specify an issue key and an assignee search string, in the form `/jira assign <issue-key> <assignee>`",
			SetupAPI:          func(api *plugintest.API) {},
		},
		"assign to valid issue but no user": {
			commandArgs: &model.CommandArgs{
				Command: "/jira assign INVALID",
				UserId:  mockUserIDWithNotifications,
			},
			expectedMsgPrefix: "Please specify an issue key and an assignee search string, in the form `/jira assign <issue-key> <assignee>`",
			SetupAPI:          func(api *plugintest.API) {},
		},
		"assign the valid issue to a non-existing user": {
			commandArgs: &model.CommandArgs{
				Command: "/jira assign VALID @unknownUser",
				UserId:  mockUserIDWithNotifications,
			},
			expectedMsgPrefix: "the mentioned user was not found",
			SetupAPI:          func(api *plugintest.API) {},
		},
		"assign the valid issue to a non-connected user": {
			commandArgs: &model.CommandArgs{
				Command: "/jira assign VALID @non_connected_user",
				UserId:  mockUserIDWithNotifications,
				UserMentions: model.UserMentionMap{
					"non_connected_user": "non_connected_user",
				},
			},
			expectedMsgPrefix: "the mentioned user is not connected to Jira",
			SetupAPI: func(api *plugintest.API) {
				api.On("LogWarn", mockAnythingOfTypeBatch("string", 5)...).Return(nil)
			},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			api := &plugintest.API{}
			defer api.AssertExpectations(t)

			tt.SetupAPI(api)
			isSendEphemeralPostCalled := false
			api.On("SendEphemeralPost", mock.AnythingOfType("string"), mock.AnythingOfType("*model.Post")).Run(func(args mock.Arguments) {
				isSendEphemeralPostCalled = true

				post := args.Get(1).(*model.Post)
				actual := strings.TrimSpace(post.Message)
				assert.True(
					t,
					strings.HasPrefix(actual, tt.expectedMsgPrefix),
					"Expected returned message to start with: \n %s\nActual:\n%s", tt.expectedMsgPrefix, actual)
			}).Once().Return(&model.Post{})

			p.SetAPI(api)
			p.instanceStore = p.getMockInstanceStoreKV(1)
			p.userStore = getMockUserStoreKV()

			cmdResponse, appError := p.ExecuteCommand(&plugin.Context{}, tt.commandArgs)
			require.Nil(t, appError)
			require.NotNil(t, cmdResponse)
			assert.True(t, isSendEphemeralPostCalled)
		})
	}
}
