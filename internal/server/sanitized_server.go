// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
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
}

func (h *SanitizedHandler) HandleSanitized(ctx context.Context, uid string) error {
	claim := &metalv1alpha1.ServerClaim{}
	claimKey := client.ObjectKey{Namespace: h.SanitizationNamespace, Name: uid}
	if err := h.Get(ctx, claimKey, claim); err != nil {
		return err
	}

	base := claim.DeepCopy()
	metav1.SetMetaDataLabel(&claim.ObjectMeta, constants.SanitizedLabel, "true")
	if err := h.Patch(ctx, claim, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patching claim %s to sanitized: %w", claimKey, err)
	}

	return nil
}

func (h *SanitizedHandler) SetupWithManager(mgr ctrl.Manager) error {
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		log := ctrl.LoggerFrom(ctx).WithValues("ServerName", "Sanitized")
		mux := http.NewServeMux()

		mux.HandleFunc("POST /sanitizations/{sanitizationUID}", func(w http.ResponseWriter, req *http.Request) {
			sanitizationUID := req.PathValue("sanitizationUID")

			log := ctrl.LoggerFrom(ctx).WithValues("uid", sanitizationUID)
			log.V(1).Info("Handle sanitized")
			handleCtx := ctrl.LoggerInto(req.Context(), log)
			if err := h.HandleSanitized(handleCtx, sanitizationUID); err != nil {
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
