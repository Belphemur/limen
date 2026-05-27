package zitadel

import (
	"context"
	"strings"
	"testing"
)

func TestCreateMachineUser_Validation(t *testing.T) {
	client := &Client{api: nil}
	ctx := context.Background()

	t.Run("empty org ID", func(t *testing.T) {
		_, err := client.CreateMachineUser(ctx, MachineUser{OrgID: "", Name: "test"})
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "OrgID") {
			t.Errorf("error should mention OrgID, got: %v", err)
		}
	})

	t.Run("empty name", func(t *testing.T) {
		_, err := client.CreateMachineUser(ctx, MachineUser{OrgID: "org-1", Name: ""})
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "Name") {
			t.Errorf("error should mention Name, got: %v", err)
		}
	})
}

func TestGetMachineUser_Validation(t *testing.T) {
	client := &Client{api: nil}
	ctx := context.Background()

	t.Run("empty user ID", func(t *testing.T) {
		_, err := client.GetMachineUser(ctx, "")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "zitadelUserID") {
			t.Errorf("error should mention zitadelUserID, got: %v", err)
		}
	})
}

func TestDeleteMachineUser_Validation(t *testing.T) {
	client := &Client{api: nil}
	ctx := context.Background()

	t.Run("empty user ID", func(t *testing.T) {
		err := client.DeleteMachineUser(ctx, "")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "zitadelUserID") {
			t.Errorf("error should mention zitadelUserID, got: %v", err)
		}
	})
}

func TestAddPersonalAccessToken_Validation(t *testing.T) {
	client := &Client{api: nil}
	ctx := context.Background()

	t.Run("empty user ID", func(t *testing.T) {
		_, _, err := client.AddPersonalAccessToken(ctx, "", nil)
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "zitadelUserID") {
			t.Errorf("error should mention zitadelUserID, got: %v", err)
		}
	})
}

func TestListPersonalAccessTokens_Validation(t *testing.T) {
	client := &Client{api: nil}
	ctx := context.Background()

	t.Run("empty user ID", func(t *testing.T) {
		_, err := client.ListPersonalAccessTokens(ctx, "")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "zitadelUserID") {
			t.Errorf("error should mention zitadelUserID, got: %v", err)
		}
	})
}

func TestRemovePersonalAccessToken_Validation(t *testing.T) {
	client := &Client{api: nil}
	ctx := context.Background()

	t.Run("empty user ID", func(t *testing.T) {
		err := client.RemovePersonalAccessToken(ctx, "", "tok-1")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "zitadelUserID") {
			t.Errorf("error should mention zitadelUserID, got: %v", err)
		}
	})

	t.Run("empty token ID", func(t *testing.T) {
		err := client.RemovePersonalAccessToken(ctx, "user-1", "")
		if err == nil {
			t.Fatal("want error, got nil")
		}
		if !strings.Contains(err.Error(), "tokenID") {
			t.Errorf("error should mention tokenID, got: %v", err)
		}
	})
}
