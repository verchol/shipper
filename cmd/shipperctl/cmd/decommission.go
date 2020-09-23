package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/bookingcom/shipper/cmd/shipperctl/configurator"
	cmdutil "github.com/bookingcom/shipper/cmd/shipperctl/util"
	shipper "github.com/bookingcom/shipper/pkg/apis/shipper/v1alpha1"
	shipperclientset "github.com/bookingcom/shipper/pkg/client/clientset/versioned"
	releaseutil "github.com/bookingcom/shipper/pkg/util/release"
)

const (
	decommissionedClustersFlagName = "decommissioned-clusters"
)

var (
	clusters []string
	dryrun   bool

	CleanCmd = &cobra.Command{
		Use:   "clean",
		Short: "clean Shipper objects",
	}

	cleanDeadClustersCmd = &cobra.Command{
		Use:   "decommissioned-clusters",
		Short: "clean Shipper releases from decommissioned clusters",
		Long: "deleting releases that are scheduled *only* on decommissioned clusters and are not contenders, " +
			"removing decommissioned clusters from annotations of releases that are scheduled partially on decommissioned clusters.",
		RunE: runCleanCommand,
	}
)

type ReleaseAndFilteredAnnotations struct {
	OldClusterAnnotation      string
	FilteredClusterAnnotation string
	Namespace                 string
	Name                      string
}

func init() {
	// Flags common to all commands under `shipperctl clean`
	CleanCmd.PersistentFlags().StringVar(&kubeConfigFile, kubeConfigFlagName, "~/.kube/config", "The path to the Kubernetes configuration file")
	if err := CleanCmd.MarkPersistentFlagFilename(kubeConfigFlagName, "yaml"); err != nil {
		CleanCmd.Printf("warning: could not mark %q for filename autocompletion: %s\n", kubeConfigFlagName, err)
	}

	CleanCmd.PersistentFlags().BoolVar(&dryrun, "dryrun", false, "If true, only prints the objects that will be modified/deleted")
	CleanCmd.PersistentFlags().StringVar(&managementClusterContext, "management-cluster-context", "", "The name of the context to use to communicate with the management cluster. defaults to the current one")
	CleanCmd.PersistentFlags().StringSliceVar(&clusters, decommissionedClustersFlagName, clusters, "List of decommissioned clusters. (Required)")
	if err := CleanCmd.MarkPersistentFlagRequired(decommissionedClustersFlagName); err != nil {
		CleanCmd.Printf("warning: could not mark %q as required: %s\n", decommissionedClustersFlagName, err)
	}

	CleanCmd.AddCommand(cleanDeadClustersCmd)
}

func runCleanCommand(cmd *cobra.Command, args []string) error {
	kubeClient, err := configurator.NewKubeClientFromKubeConfig(kubeConfigFile, managementClusterContext)
	if err != nil {
		return err
	}

	shipperClient, err := configurator.NewShipperClientFromKubeConfig(kubeConfigFile, managementClusterContext)
	if err != nil {
		return err
	}

	deleteReleases, updateAnnotations, err := collectReleases(kubeClient, shipperClient)
	if err != nil {
		return err
	}

	err = reviewActions(cmd, deleteReleases, updateAnnotations)
	if err != nil {
		return err
	}

	var errList []string
	if len(errList) > 0 {
		return fmt.Errorf(strings.Join(errList, ","))
	}
	return nil
}

func collectReleases(kubeClient kubernetes.Interface, shipperClient shipperclientset.Interface) ([]ReleaseAndFilteredAnnotations, []ReleaseAndFilteredAnnotations, error) {
	var deleteReleases []ReleaseAndFilteredAnnotations
	var updateAnnotations []ReleaseAndFilteredAnnotations
	namespaceList, err := kubeClient.CoreV1().Namespaces().List(metav1.ListOptions{})
	if err != nil {
		return nil, nil, err
	}
	var errList []string
	for _, ns := range namespaceList.Items {
		releaseList, err := shipperClient.ShipperV1alpha1().Releases(ns.Name).List(metav1.ListOptions{})
		if err != nil {
			errList = append(errList, err.Error())
			continue
		}
		for _, rel := range releaseList.Items {
			selectedClusters := releaseutil.GetSelectedClusters(&rel)
			filteredClusters := cmdutil.FilterSelectedClusters(releaseutil.GetSelectedClusters(&rel), clusters)
			if len(filteredClusters) > 0 {
				sort.Strings(filteredClusters)

				filteredClusterAnnotation := strings.Join(filteredClusters, ",")
				if filteredClusterAnnotation == rel.Annotations[shipper.ReleaseClustersAnnotation] {
					continue
				}
				//rel.Annotations[shipper.ReleaseClustersAnnotation] = strings.Join(filteredClusters, ",")
				//cmd.Printf("Editing annotations of release %s/%s to %s...", rel.Namespace, rel.Name, rel.Annotations[shipper.ReleaseClustersAnnotation])
				//if !dryrun {
				//	_, err := shipperClient.ShipperV1alpha1().Releases(ns.Name).Update(&rel)
				//	if err != nil {
				//		errList = append(errList, err.Error())
				//	}
				//	cmd.Println("done")
				//} else {
				//	cmd.Println("dryrun")
				//}
				updateAnnotations = append(
					updateAnnotations,
					ReleaseAndFilteredAnnotations{
						OldClusterAnnotation:      strings.Join(selectedClusters, ","),
						FilteredClusterAnnotation: filteredClusterAnnotation,
						Namespace:                 rel.Namespace,
						Name:                      rel.Name,
					})
				continue
			}
			isContender, err := cmdutil.IsContender(&rel, shipperClient)
			if err != nil {
				errList = append(errList, err.Error())
				continue
			}
			if len(filteredClusters) == 0 && !isContender {
				//cmd.Printf("Deleting release %s/%s...", rel.Namespace, rel.Name)
				//if !dryrun {
				//	err := shipperClient.ShipperV1alpha1().Releases(ns.Name).Delete(rel.Name, &metav1.DeleteOptions{})
				//	if err != nil {
				//		errList = append(errList, err.Error())
				//	}
				//	cmd.Println("done")
				//} else {
				//	cmd.Println("dryrun")
				//}
				outputRelease := ReleaseAndFilteredAnnotations{
					OldClusterAnnotation:      strings.Join(selectedClusters, ","),
					FilteredClusterAnnotation: "",
					Namespace:                 rel.Namespace,
					Name:                      rel.Name,
				}
				deleteReleases = append(deleteReleases, outputRelease)
			}
		}
	}
	return deleteReleases, updateAnnotations, fmt.Errorf(strings.Join(errList, ","))
}

func reviewActions(cmd *cobra.Command, deleteReleases []ReleaseAndFilteredAnnotations, updateAnnotations []ReleaseAndFilteredAnnotations) error {
	cmd.Printf("About to edit %d releases and and delete %d releases\n", len(updateAnnotations), len(deleteReleases))
	confirm, err := cmdutil.AskForConfirmation(os.Stdin, "Would you like to see the releases? (This will not start the process)")
	if err != nil {
		return err
	}
	if confirm {
		tt := &metav1.Table{
			TypeMeta: metav1.TypeMeta{},
			ListMeta: metav1.ListMeta{},
			ColumnDefinitions: []metav1.TableColumnDefinition{
				{
					Name: "Namespace",
					Type: "string",
				},
				{
					Name: "Name",
					Type: "string",
				},

				{
					Name: "Action Taken",
					Type: "string",
				},

				{
					Name: "Old Clusters Annotations",
					Type: "string",
				},

				{
					Name: "New Clusters Annotations",
					Type: "string",
				},
			},
			Rows: nil,
		}
		//tbl, err := prettytable.NewTable([]prettytable.Column{
		//	{Header: "NAMESPACE"},
		//	{Header: "NAME"},
		//	{Header: "ACTION TAKEN"},
		//	{Header: "OLD CLUSTER ANNOTATIONS"},
		//	{Header: "NEW CLUSTER ANNOTATIONS"},
		//}...)
		//if err != nil {
		//	return err
		//}
		for _, release := range deleteReleases {
			tt.Rows = append(tt.Rows, metav1.TableRow{
				Cells: []interface{}{
					release.Namespace,
					release.Name,
					" DELETE ",
					release.OldClusterAnnotation,
					release.FilteredClusterAnnotation,
				},
			})
			//tbl.AddRow(
			//	release.Namespace,
			//	release.Name,
			//	" DELETE ",
			//	release.OldClusterAnnotation,
			//	release.FilteredClusterAnnotation,
			//)
		}
		for _, release := range updateAnnotations {
			tt.Rows = append(tt.Rows, metav1.TableRow{
				Cells: []interface{}{
					release.Namespace,
					release.Name,
					"UPDATE",
					release.OldClusterAnnotation,
					release.FilteredClusterAnnotation,
				},
			})
			//tbl.AddRow(
			//	release.Namespace,
			//	release.Name,
			//	"UPDATE",
			//	release.OldClusterAnnotation,
			//	release.FilteredClusterAnnotation,
			//)
		}
		//tbl.Print()
		//printer := printers.NewTablePrinter(printers.PrintOptions{})
		//printer.PrintObj(tt, cmd.OutOrStdout())
		//cmd.Println()
	}
	return nil
}
