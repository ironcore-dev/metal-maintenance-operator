// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/token"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

type SanitizedHandler struct {
	client.Client
	SanitizationNamespace string
	Address               string
	SigningKey             []byte
}

func (h *SanitizedHandler) HandleSanitized(ctx context.Context, uid string, bearerToken string) error {
	claim := &metalv1alpha1.ServerClaim{}
	claimKey := client.ObjectKey{Namespace: h.SanitizationNamespace, Name: uid}
	if err := h.Get(ctx, claimKey, claim); err != nil {
		return err
	}

	if len(h.SigningKey) > 0 {
		if bearerToken == "" {
			return errUnauthorized
		}
		systemUUID, _, valid, err := token.VerifySignedDiscoveryToken(h.SigningKey, bearerToken)
		if err != nil {
			return fmt.Errorf("verifying sanitization token: %w", err)
		}
		if !valid {
			return errUnauthorized
		}

		// Cross-check: the token must be bound to the server referenced by this claim.
		if claim.Spec.ServerRef != nil {
			server := &metalv1alpha1.Server{}
			serverKey := client.ObjectKey{Name: claim.Spec.ServerRef.Name}
			if err := h.Get(ctx, serverKey, server); err != nil {
				return fmt.Errorf("looking up server for claim: %w", err)
			}
			if server.Spec.SystemUUID != systemUUID {
				return errUnauthorized
			}
		}
	}

	base := claim.DeepCopy()
	metav1.SetMetaDataLabel(&claim.ObjectMeta, constants.SanitizedLabel, "true")
	if err := h.Patch(ctx, claim, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patching claim %s to sanitized: %w", claimKey, err)
	}

	return nil
}

var errUnauthorized = errors.New("unauthorized")

func (h *SanitizedHandler) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		log := ctrl.LoggerFrom(ctx).WithValues("ServerName", "Sanitized")
		mux := http.NewServeMux()

		mux.HandleFunc("POST /sanitizations/{sanitizationUID}", func(w http.ResponseWriter, req *http.Request) {
			sanitizationUID := req.PathValue("sanitizationUID")

			log := ctrl.LoggerFrom(ctx).WithValues("uid", sanitizationUID)
			log.V(1).Info("Handle sanitized")
			handleCtx := ctrl.LoggerInto(req.Context(), log)

			bearerToken := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")

			if err := h.HandleSanitized(handleCtx, sanitizationUID, bearerToken); err != nil {
				if errors.Is(err, errUnauthorized) {
					http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
					return
				}
				if !apierrors.IsNotFound(err) {
					log.Error(err, "Error handling sanitized")
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
					return
				}

				http.Error(w, "Claim not found", http.StatusNotFound)
				return
			}

			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintln(w, "Marked claim as sanitized")
		})

		srv := http.Server{
			Addr:    h.Address,
			Handler: mux,
		}

		res := make(chan error, 1)
		go func() {
			res <- func() error {
				log.Info("Starting server")
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					return fmt.Errorf("listening / serving: %w", err)
				}
				return nil
			}()
		}()

		select {
		case <-ctx.Done():
			log.Info("Shutting down server")
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			return <-res
		case err := <-res:
			if err != nil {
				return err
			}
			return fmt.Errorf("server returned early")
		}
	}))
}
