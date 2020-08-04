package integration

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
)

// https://bugzilla.redhat.com/show_bug.cgi?id=1750665
// https://bugzilla.redhat.com/show_bug.cgi?id=1753755
func TestDefaultUploadFrequency(t *testing.T) {
	// Backup support secret from openshift-config namespace.
	// oc extract secret/support -n openshift-config --to=.
	supportSecret, err := clientset.CoreV1().Secrets(OpenShiftConfig).Get(Support, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("The support secret read failed: %s", err)
	}
	resetSecrets := func() {
		err = forceUpdateSecret(OpenShiftConfig, Support, supportSecret)
		if err != nil {
			t.Error(err)
		}
	}
	defer func() {
		resetSecrets()
	}()
	// delete any existing overriding secret
	err = clientset.CoreV1().Secrets(OpenShiftConfig).Delete(Support, &metav1.DeleteOptions{})

	// if the secret is not found, continue, not a problem
	if err != nil && err.Error() != `secrets "support" not found` {
		t.Fatal(err.Error())
	}

	// restart insights-operator (delete pods)
	restartInsightsOperator(t)

	// check logs for "Gathering cluster info every 2h0m0s"
	checkPodsLogs(t, clientset, "Gathering cluster info every 2h0m0s")

	// verify it's possible to override it
	newSecret := corev1.Secret{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      Support,
			Namespace: OpenShiftConfig,
		},
		Data: map[string][]byte{
			"interval": []byte("3m"),
		},
		Type: "Opaque",
	}

	_, err = clientset.CoreV1().Secrets(OpenShiftConfig).Create(&newSecret)
	if err != nil {
		t.Fatal(err.Error())
	}
	// restart insights-operator (delete pods)
	restartInsightsOperator(t)

	// check logs for "Gathering cluster info every 3m0s"
	checkPodsLogs(t, clientset, "Gathering cluster info every 3m0s")
}

// TestUnreachableHost checks if insights operator reports "degraded" after 5 unsuccessful upload attempts
// This tests takes about 317 s
// https://bugzilla.redhat.com/show_bug.cgi?id=1745973
func TestUnreachableHost(t *testing.T) {
	supportSecret, err := clientset.CoreV1().Secrets(OpenShiftConfig).Get(Support, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("The support secret read failed: %s", err)
	}
	resetSecrets := func() {
		err = forceUpdateSecret(OpenShiftConfig, Support, supportSecret)
		if err != nil {
			t.Error(err)
		}
	}
	defer func() {
		resetSecrets()
	}()
	// Replace the endpoint to some not valid url.
	// oc -n openshift-config create secret generic support --from-literal=endpoint=http://localhost --dry-run -o yaml | oc apply -f - -n openshift-config
	modifiedSecret := corev1.Secret{
		TypeMeta: metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{
			Name:      Support,
			Namespace: OpenShiftConfig,
		},
		Data: map[string][]byte{
			"endpoint": []byte("http://localhost"),
			"interval": []byte("1m"), // for faster testing
		},
		Type: "Opaque",
	}
	// delete any existing overriding secret
	err = clientset.CoreV1().Secrets(OpenShiftConfig).Delete(Support, &metav1.DeleteOptions{})

	// if the secret is not found, continue, not a problem
	if err != nil && err.Error() != `secrets "support" not found` {
		t.Fatal(err.Error())
	}
	_, err = clientset.CoreV1().Secrets(OpenShiftConfig).Create(&modifiedSecret)
	if err != nil {
		t.Fatal(err.Error())
	}
	// Restart insights-operator
	// oc delete pods --namespace=openshift-insights --all
	restartInsightsOperator(t)

	// Check the logs
	checkPodsLogs(t, clientset, "exceeded than threshold 5. Marking as degraded.")

	// Check the operator is degraded
	insightsDegraded := isOperatorDegraded(t, clusterOperatorInsights())
	if !insightsDegraded {
		t.Fatal("Insights is not degraded")
	}
	// Delete secret
	err = clientset.CoreV1().Secrets(OpenShiftConfig).Delete(Support, &metav1.DeleteOptions{})
	if err != nil {
		t.Fatal(err.Error())
	}
	// Check the operator is not degraded anymore
	errDegraded := wait.PollImmediate(3*time.Second, 3*time.Minute, func() (bool, error) {
		insightsDegraded := isOperatorDegraded(t, clusterOperatorInsights())
		if insightsDegraded {
			return false, nil
		}
		return true, nil
	})
	t.Log(errDegraded)
}
// xd nene
//https://bugzilla.redhat.com/show_bug.cgi?id=1838973
func latestArchiveContainsPodLogs(t *testing.T) {
	logCount, err :=  latestArchiveContainsFiles(t, `^config/pod/openshift-monitoring/logs/.*\.log$`)
	e(t, err, "Checking for log files failed")
	t.Log(logCount, "log files found")
	if logCount == 0 {
		t.Error("no log files in archive")
	}
}

//https://bugzilla.redhat.com/show_bug.cgi?id=1767719
func latestArchiveContainsEvent(t *testing.T) {
	logCount, err :=  latestArchiveContainsFiles(t, `^events/openshift-monitoring.json$`)
	e(t, err, "Checking for event failed")
	t.Log(logCount, "event files found")
	if logCount == 0 {
		t.Error("no event file in archive")
	}
}

func TestCollectingAfterDegradingOperator(t *testing.T) {
	defer ChangeReportTimeInterval(t, 1)()
	defer degradeOperatorMonitoring(t)()
	checkPodsLogs(t, clientset, `Wrote \d+ records to disk in \d+`, true)
	t.Run("Logs", latestArchiveContainsPodLogs)
	t.Run("Event", latestArchiveContainsEvent)
}

// https://bugzilla.redhat.com/show_bug.cgi?id=1782151
func TestClusterDefaultNodeSelector(t *testing.T) {
	// set default selector of node-role.kubernetes.io/worker
	schedulers, err := configClient.Schedulers().List(metav1.ListOptions{})
	if err != nil {
		t.Fatal(err.Error())
	}
	for _, scheduler := range schedulers.Items {
		if scheduler.ObjectMeta.Name == "cluster" {
			scheduler.Spec.DefaultNodeSelector = "node-role.kubernetes.io/worker="
			configClient.Schedulers().Update(&scheduler)
		}
	}

	// restart insights-operator (delete pods)
	restartInsightsOperator(t)

	// check the pod is scheduled
	newPods, err := clientset.CoreV1().Pods("openshift-insights").List(metav1.ListOptions{})
	if err != nil {
		t.Fatal(err.Error())
	}

	for _, newPod := range newPods.Items {
		pod, err := clientset.CoreV1().Pods("openshift-insights").Get(newPod.Name, metav1.GetOptions{})
		if err != nil {
			panic(err.Error())
		}
		podConditions := pod.Status.Conditions
		for _, condition := range podConditions {
			if condition.Type == "PodScheduled" {
				if condition.Status != "True" {
					t.Log("Pod is not scheduled")
					t.Fatal(err.Error())
				}
			}
		}
		t.Log("Pod is scheduled")
	}
}
