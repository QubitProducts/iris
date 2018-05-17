package cmd

import (
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/client-go/tools/clientcmd"
)

var configOverrides clientcmd.ConfigOverrides

func GetCommands() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:   "iris",
		Short: "Envoy Kubernetes xDS",
		Long:  "Envoy Kubernetes xDS",
	}

	clientcmd.BindOverrideFlags(&configOverrides, rootCmd.PersistentFlags(), clientcmd.RecommendedConfigOverrideFlags(""))
	rootCmd.AddCommand(createServeCommand())
	return rootCmd
}

func getKubeClient() (*kubernetes.Clientset, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	kubeconfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, &configOverrides)

	clientConf, err := kubeconfig.ClientConfig()
	if err != nil {
		return nil, err
	}
	clientConf.ContentType = "application/vnd.kubernetes.protobuf"
	return kubernetes.NewForConfig(clientConf)
}
