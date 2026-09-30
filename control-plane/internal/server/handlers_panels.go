package server

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/projects/bootstrap"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
)

// Recipe writes from the Recipe document (phase 2 · wave 2 · stream U): a schema-driven form (an augmentation
// profile) commits its file to main like the other repository edits a person makes.

func (c commandResponse) VisitRecipesNewResponse(w http.ResponseWriter) error  { return c.write(w) }
func (c commandResponse) VisitRecipesEditResponse(w http.ResponseWriter) error { return c.write(w) }

// RecipesNew implements recipes.new.
func (s *Server) RecipesNew(ctx context.Context, req api.RecipesNewRequestObject) (api.RecipesNewResponseObject, error) {
	return s.writeRecipe(ctx, req.P, "recipes.new", req.Params.IdempotencyKey, req.Params.DryRun, http.StatusCreated,
		bootstrap.FileWrite{Path: req.Body.Path, Content: []byte(req.Body.Content), Message: deref(req.Body.Message)})
}

// RecipesEdit implements recipes.edit: If-Match is the commit that last changed the file.
func (s *Server) RecipesEdit(ctx context.Context, req api.RecipesEditRequestObject) (api.RecipesEditResponseObject, error) {
	expect, err := headOf(req.Params.IfMatch)
	if err != nil {
		return nil, err
	}
	return s.writeRecipe(ctx, req.P, "recipes.edit", req.Params.IdempotencyKey, req.Params.DryRun, http.StatusOK,
		bootstrap.FileWrite{Path: req.Path, Content: []byte(req.Body.Content), Expect: expect, Message: deref(req.Body.Message)})
}

func (s *Server) writeRecipe(ctx context.Context, slug, op, key string, dry *bool, status int, w bootstrap.FileWrite) (commandResponse, error) {
	_, svc, err := s.projectRepo(ctx, slug)
	if err != nil {
		return commandResponse{}, err
	}
	ctx, err = s.withProject(ctx, slug)
	if err != nil {
		return commandResponse{}, err
	}
	cmd := command(ctx, op, key, dry)
	return s.run(ctx, cmd, func(ctx context.Context, tx pgx.Tx) (commands.Result, []events.Draft, error) {
		p, err := projects.Get(ctx, tx, slug)
		if err != nil {
			return commands.Result{}, nil, err
		}
		c, drafts, err := svc.WriteFile(ctx, tx, p, w, cmd.Actor, cmd.DryRun)
		if err != nil {
			return commands.Result{}, nil, err
		}
		body, err := s.recipeAt(ctx, svc, p.Slug, w)
		if err != nil {
			return commands.Result{}, nil, err
		}
		if c.SHA == "" { // a dry run, or a write that changed nothing: show the content as it would be
			body.Content, body.Encoding, body.Bytes = string(w.Content), api.Utf8, len(w.Content)
		}
		return commands.Result{Status: status, Body: body}, drafts, nil
	})
}

// recipeAt reads the file on main with its history as recipes.get answers it; a file that does not exist yet (a dry
// run of recipes.new) answers an empty history at main's head.
func (s *Server) recipeAt(ctx context.Context, svc *bootstrap.Service, slug string, w bootstrap.FileWrite) (api.Recipe, error) {
	res, err := s.RecipesGet(ctx, api.RecipesGetRequestObject{P: slug, Path: w.Path})
	if err == nil {
		if r, ok := res.(api.RecipesGet200JSONResponse); ok {
			return api.Recipe(r), nil
		}
	}
	head, herr := svc.Repos().Head(ctx, slug)
	if herr != nil {
		return api.Recipe{}, herr
	}
	return api.Recipe{Path: w.Path, Ref: repos.Main, Commit: head, History: []api.RecipeCommit{}}, nil
}
