package selectivedeployment

import (
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	selectivedeployment_v1 "headnode/pkg/apis/selectivedeployment/v1alpha"
	"headnode/pkg/authorization"
	selectivedeploymentinformer_v1 "headnode/pkg/client/informers/externalversions/selectivedeployment/v1alpha"
	"headnode/pkg/node"

	log "github.com/Sirupsen/logrus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// The main structure of controller
type controller struct {
	logger         *log.Entry
	queue          workqueue.RateLimitingInterface
	informer       cache.SharedIndexInformer
	nodeInformer   cache.SharedIndexInformer
	deplInformer   cache.SharedIndexInformer
	daemonInformer cache.SharedIndexInformer
	stateInformer  cache.SharedIndexInformer
	handler        HandlerInterface
	wg             map[string]*sync.WaitGroup
}

// The main structure of informerEvent
type informerevent struct {
	key      string
	function string
	delta    string
}

// Definitions of the state of the selectivedeployment resource (failure, partial, success)
const failure = "Failure"
const partial = "Running Partially"
const success = "Running"
const noSchedule = "NoSchedule"
const create = "create"
const update = "update"
const delete = "delete"
const charset = "abcdefghijklmnopqrstuvwxyz" +
	"ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
const trueStr = "True"
const falseStr = "False"
const unknownStr = "Unknown"

// Start function is entry point of the controller
func Start() {
	clientset, err := authorization.CreateClientSet()
	if err != nil {
		log.Println(err.Error())
		panic(err.Error())
	}
	sdClientset, err := authorization.CreateSelectiveDeploymentClientSet()
	if err != nil {
		log.Println(err.Error())
		panic(err.Error())
	}

	wg := make(map[string]*sync.WaitGroup)
	sdHandler := &SDHandler{}
	// Create the selectivedeployment informer which was generated by the code generator to list and watch selectivedeployment resources
	informer := selectivedeploymentinformer_v1.NewSelectiveDeploymentInformer(
		sdClientset,
		metav1.NamespaceAll,
		0,
		cache.Indexers{},
	)
	// Create a work queue which contains a key of the resource to be handled by the handler
	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())
	var event informerevent
	// Event handlers deal with events of resources. In here, we take into consideration of adding and updating nodes
	informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			// Put the resource object into a key
			event.key, err = cache.MetaNamespaceKeyFunc(obj)
			event.function = create
			log.Infof("Add selectivedeployment: %s", event.key)
			if err == nil {
				// Add the key to the queue
				queue.Add(event)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if reflect.DeepEqual(oldObj.(*selectivedeployment_v1.SelectiveDeployment).Status, newObj.(*selectivedeployment_v1.SelectiveDeployment).Status) {
				event.key, err = cache.MetaNamespaceKeyFunc(newObj)
				event.function = update
				// The variable of event.delta contains the different values of the old object from the new one
				event.delta = fmt.Sprintf("%s", strings.Join(dry(oldObj.(*selectivedeployment_v1.SelectiveDeployment).Spec.Controller, newObj.(*selectivedeployment_v1.SelectiveDeployment).Spec.Controller), "/?delta?/ "))
				log.Infof("Update selectivedeployment: %s", event.key)
				if err == nil {
					queue.Add(event)
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			// DeletionHandlingMetaNamsespaceKeyFunc helps to check the existence of the object while it is still contained in the index.
			// Put the resource object into a key
			event.key, err = cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			event.function = delete
			// The variable of event.delta contains the different values in the same way as UpdateFunc.
			// In addition to that, this variable includes the name, namespace, type, controller of the deleted object.
			event.delta = fmt.Sprintf("%s-?delta?- %s-?delta?- %s-?delta?- %s", obj.(*selectivedeployment_v1.SelectiveDeployment).GetName(), obj.(*selectivedeployment_v1.SelectiveDeployment).GetNamespace(), obj.(*selectivedeployment_v1.SelectiveDeployment).Spec.Type,
				strings.Join(dry(obj.(*selectivedeployment_v1.SelectiveDeployment).Spec.Controller, []selectivedeployment_v1.Controller{}), "/?delta?/ "))
			log.Infof("Delete selectivedeployment: %s", event.key)
			if err == nil {
				queue.Add(event)
			}
		},
	})

	// The selectivedeployment resources are reconfigured according to node events in this section
	nodeInformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			// The main purpose of listing is to attach geo labels to whole nodes at the beginning
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return clientset.CoreV1().Nodes().List(options)
			},
			// This function watches all changes/updates of nodes
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return clientset.CoreV1().Nodes().Watch(options)
			},
		},
		&corev1.Node{},
		0,
		cache.Indexers{},
	)
	nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			nodeObj := obj.(*corev1.Node)
			for _, conditionRow := range nodeObj.Status.Conditions {
				if conditionType := conditionRow.Type; conditionType == "Ready" {
					if conditionRow.Status == trueStr {
						key, err := cache.MetaNamespaceKeyFunc(obj)
						if err != nil {
							log.Println(err.Error())
							panic(err.Error())
						}
						sdRaw, _ := sdClientset.EdgenetV1alpha().SelectiveDeployments("").List(metav1.ListOptions{})
						for _, sdRow := range sdRaw.Items {
							if sdRow.Status.State == partial || sdRow.Status.State == success {
							selectorLoop:
								for _, selectorDet := range sdRow.Spec.Selector {
									if selectorDet.Count == 0 || (selectorDet.Count != 0 && (strings.Contains(sdRow.Status.Message, "Fewer nodes issue") || strings.Contains(sdRow.Status.Message, "fewer nodes issue"))) {
										event.key, err = cache.MetaNamespaceKeyFunc(sdRow.DeepCopyObject())
										event.function = create
										log.Infof("SD node added: %s, recovery started for: %s", key, event.key)
										if err == nil {
											queue.Add(event)
										}
										break selectorLoop
									}
								}
							}
						}
					}
				}
			}
		},
		UpdateFunc: func(old, new interface{}) {
			oldObj := old.(*corev1.Node)
			newObj := new.(*corev1.Node)
			oldReady := node.GetConditionReadyStatus(oldObj)
			newReady := node.GetConditionReadyStatus(newObj)
			if (oldReady == falseStr && newReady == trueStr) ||
				(oldReady == unknownStr && newReady == trueStr) ||
				(oldObj.Spec.Unschedulable == true && newObj.Spec.Unschedulable == false) {
				key, err := cache.MetaNamespaceKeyFunc(newObj)
				if err != nil {
					log.Println(err.Error())
					panic(err.Error())
				}
				sdRaw, _ := sdClientset.EdgenetV1alpha().SelectiveDeployments("").List(metav1.ListOptions{})
				for _, sdRow := range sdRaw.Items {
					if sdRow.Status.State == partial || sdRow.Status.State == success {
					selectorLoop:
						for _, selectorDet := range sdRow.Spec.Selector {
							if selectorDet.Count == 0 || (selectorDet.Count != 0 && (strings.Contains(sdRow.Status.Message, "Fewer nodes issue") || strings.Contains(sdRow.Status.Message, "fewer nodes issue"))) {
								event.key, err = cache.MetaNamespaceKeyFunc(sdRow.DeepCopyObject())
								event.function = create
								log.Infof("SD node updated: %s, recovery started for: %s", key, event.key)
								if err == nil {
									queue.Add(event)
								}
								break selectorLoop
							}
						}
					}
				}
			} else if updated := node.CompareIPAddresses(oldObj, newObj); (oldReady == trueStr && newReady == falseStr) ||
				(oldReady == trueStr && newReady == unknownStr) ||
				(oldObj.Spec.Unschedulable == false && newObj.Spec.Unschedulable == true) ||
				(newObj.Spec.Unschedulable == false && newReady == trueStr && updated) {
				key, err := cache.MetaNamespaceKeyFunc(newObj.DeepCopyObject())
				if err != nil {
					log.Println(err.Error())
					panic(err.Error())
				}
				ownerList, status := sdHandler.GetSelectiveDeployments(newObj.GetName())
				if status {
					for _, ownerDet := range ownerList {
						sdObj, err := sdClientset.EdgenetV1alpha().SelectiveDeployments(ownerDet[0]).Get(ownerDet[1], metav1.GetOptions{})
						if err != nil {
							continue
						}
						event.key, err = cache.MetaNamespaceKeyFunc(sdObj.DeepCopyObject())
						event.function = create
						log.Infof("SD node updated: %s, recovery started for: %s", key, event.key)
						if err == nil {
							queue.Add(event)
						}
					}
				}
			}
		},
		DeleteFunc: func(obj interface{}) {
			nodeObj := obj.(*corev1.Node)
			key, err := cache.MetaNamespaceKeyFunc(obj)
			if err != nil {
				log.Println(err.Error())
				panic(err.Error())
			}
			ownerList, status := sdHandler.GetSelectiveDeployments(nodeObj.GetName())
			if status {
				for _, ownerDet := range ownerList {
					sdObj, err := sdClientset.EdgenetV1alpha().SelectiveDeployments(ownerDet[0]).Get(ownerDet[1], metav1.GetOptions{})
					if err != nil {
						log.Println(err.Error())
						continue
					}
					event.key, err = cache.MetaNamespaceKeyFunc(sdObj.DeepCopyObject())
					event.function = create
					log.Infof("SD node deleted: %s, recovery started for: %s", key, event.key)
					if err == nil {
						queue.Add(event)
					}
				}
			}
		},
	})

	// The selectivedeployment resources are reconfigured according to controller events in this section
	addSDToQueue := func(sdSlice []selectivedeployment_v1.SelectiveDeployment, key string, ctlType string) {
		for _, sdRow := range sdSlice {
			event.key, err = cache.MetaNamespaceKeyFunc(sdRow.DeepCopyObject())
			event.function = create
			log.Infof("SD %s added: %s, recovery started for: %s", ctlType, key, event.key)
			if err == nil {
				queue.Add(event)
			}
		}
	}
	controllerAddFunc := func(obj interface{}) {
		sdSlice, status := sdHandler.CheckControllerStatus(nil, obj, create)
		if status {
			switch controllerObj := obj.(type) {
			case *appsv1.Deployment:
				ctlObj := controllerObj
				key, _ := cache.MetaNamespaceKeyFunc(ctlObj)
				addSDToQueue(sdSlice, key, "Deployment")
			case *appsv1.DaemonSet:
				ctlObj := controllerObj
				key, _ := cache.MetaNamespaceKeyFunc(ctlObj)
				addSDToQueue(sdSlice, key, "DaemonSet")
			case *appsv1.StatefulSet:
				ctlObj := controllerObj
				key, _ := cache.MetaNamespaceKeyFunc(ctlObj)
				addSDToQueue(sdSlice, key, "StatefulSet")
			}
		}
	}
	controllerUpdateFunc := func(old, new interface{}) {
		switch new.(type) {
		case *appsv1.Deployment:
			newCtl := new.(*appsv1.Deployment).DeepCopy()
			oldCtl := old.(*appsv1.Deployment).DeepCopy()
			if newCtl.ResourceVersion == oldCtl.ResourceVersion {
				// Periodic resync will send update events for all known Deployments.
				// Two different versions of the same Deployments will always have different RVs.
				return
			}
			_, status := sdHandler.CheckControllerStatus(old, new, update)
			if status {
				key, _ := cache.MetaNamespaceKeyFunc(newCtl)
				log.Infof("SD Deployment updated, recovery started: %s", key)
				newCtl.Spec.Template.Spec.Affinity = oldCtl.Spec.Template.Spec.Affinity
				newCtl.ObjectMeta.Annotations["kubectl.kubernetes.io/last-applied-configuration"] = ""
				newCtl.SetResourceVersion("")
				clientset.AppsV1().Deployments(newCtl.GetNamespace()).Update(newCtl)
			}
		case *appsv1.DaemonSet:
			newCtl := new.(*appsv1.DaemonSet).DeepCopy()
			oldCtl := old.(*appsv1.DaemonSet).DeepCopy()
			if newCtl.ResourceVersion == oldCtl.ResourceVersion {
				return
			}
			_, status := sdHandler.CheckControllerStatus(old, new, update)
			if status {
				key, _ := cache.MetaNamespaceKeyFunc(newCtl)
				log.Infof("SD DaemonSet updated, recovery started: %s", key)
				newCtl.Spec.Template.Spec.Affinity = oldCtl.Spec.Template.Spec.Affinity
				newCtl.ObjectMeta.Annotations["kubectl.kubernetes.io/last-applied-configuration"] = ""
				newCtl.SetResourceVersion("")
				clientset.AppsV1().DaemonSets(newCtl.GetNamespace()).Update(newCtl)
			}
		case *appsv1.StatefulSet:
			newCtl := new.(*appsv1.StatefulSet).DeepCopy()
			oldCtl := old.(*appsv1.StatefulSet).DeepCopy()
			if newCtl.ResourceVersion == oldCtl.ResourceVersion {
				return
			}
			_, status := sdHandler.CheckControllerStatus(old, new, update)
			if status {
				key, _ := cache.MetaNamespaceKeyFunc(newCtl)
				log.Infof("SD StatefulSet updated, recovery started: %s", key)
				newCtl.Spec.Template.Spec.Affinity = oldCtl.Spec.Template.Spec.Affinity
				newCtl.ObjectMeta.Annotations["kubectl.kubernetes.io/last-applied-configuration"] = ""
				newCtl.SetResourceVersion("")
				clientset.AppsV1().StatefulSets(newCtl.GetNamespace()).Update(newCtl)
			}
		}
	}
	controllerDeleteFunc := func(obj interface{}) {
		sdSlice, status := sdHandler.CheckControllerStatus(nil, obj, delete)
		if status {
			ownerReferences := []metav1.OwnerReference{}
			for _, sdRow := range sdSlice {
				controllerRef := *metav1.NewControllerRef(sdRow.DeepCopy(), selectivedeployment_v1.SchemeGroupVersion.WithKind("SelectiveDeployment"))
				takeControl := false
				controllerRef.Controller = &takeControl
				ownerReferences = append(ownerReferences, controllerRef)
			}
			switch controllerObj := obj.(type) {
			case *appsv1.Deployment:
				ctlObj := controllerObj.DeepCopy()
				key, _ := cache.MetaNamespaceKeyFunc(ctlObj)
				ctlObj.SetResourceVersion("")
				ctlObj.ObjectMeta.OwnerReferences = ownerReferences
				if len(ctlObj.ObjectMeta.OwnerReferences) > 0 {
					log.Infof("SD Deployment deleted, recovery started: %s", key)
					clientset.AppsV1().Deployments(ctlObj.GetNamespace()).Create(ctlObj)
				}
			case *appsv1.DaemonSet:
				ctlObj := controllerObj.DeepCopy()
				key, _ := cache.MetaNamespaceKeyFunc(ctlObj)
				ctlObj.SetResourceVersion("")
				ctlObj.ObjectMeta.OwnerReferences = ownerReferences
				if len(ctlObj.ObjectMeta.OwnerReferences) > 0 {
					log.Infof("SD DaemonSet deleted, recovery started: %s", key)
					clientset.AppsV1().DaemonSets(ctlObj.GetNamespace()).Create(ctlObj)
				}
			case *appsv1.StatefulSet:
				ctlObj := controllerObj.DeepCopy()
				key, _ := cache.MetaNamespaceKeyFunc(ctlObj)
				ctlObj.SetResourceVersion("")
				ctlObj.ObjectMeta.OwnerReferences = ownerReferences
				if len(ctlObj.ObjectMeta.OwnerReferences) > 0 {
					log.Infof("SD StatefulSet deleted, recovery started: %s", key)
					clientset.AppsV1().StatefulSets(ctlObj.GetNamespace()).Create(ctlObj)
				}
			}
		}
	}
	deploymentInformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return clientset.AppsV1().Deployments("").List(options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return clientset.AppsV1().Deployments("").Watch(options)
			},
		},
		&appsv1.Deployment{},
		0,
		cache.Indexers{},
	)
	deploymentInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    controllerAddFunc,
		UpdateFunc: controllerUpdateFunc,
		DeleteFunc: controllerDeleteFunc,
	})
	daemonSetInformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return clientset.AppsV1().DaemonSets("").List(options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return clientset.AppsV1().DaemonSets("").Watch(options)
			},
		},
		&appsv1.DaemonSet{},
		0,
		cache.Indexers{},
	)
	daemonSetInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    controllerAddFunc,
		UpdateFunc: controllerUpdateFunc,
		DeleteFunc: controllerDeleteFunc,
	})
	statefulSetInformer := cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return clientset.AppsV1().StatefulSets("").List(options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return clientset.AppsV1().StatefulSets("").Watch(options)
			},
		},
		&appsv1.StatefulSet{},
		0,
		cache.Indexers{},
	)
	statefulSetInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    controllerAddFunc,
		UpdateFunc: controllerUpdateFunc,
		DeleteFunc: controllerDeleteFunc,
	})
	controller := controller{
		logger:         log.NewEntry(log.New()),
		informer:       informer,
		nodeInformer:   nodeInformer,
		deplInformer:   deploymentInformer,
		daemonInformer: daemonSetInformer,
		stateInformer:  statefulSetInformer,
		queue:          queue,
		handler:        sdHandler,
		wg:             wg,
	}

	// A channel to terminate elegantly
	stopCh := make(chan struct{})
	defer close(stopCh)
	// Run the controller loop as a background task to start processing resources
	go controller.run(stopCh)
	// A channel to observe OS signals for smooth shut down
	sigTerm := make(chan os.Signal, 1)
	signal.Notify(sigTerm, syscall.SIGTERM)
	signal.Notify(sigTerm, syscall.SIGINT)
	<-sigTerm
}

// Run starts the controller loop
func (c *controller) run(stopCh <-chan struct{}) {
	// A Go panic which includes logging and terminating
	defer utilruntime.HandleCrash()
	// Shutdown after all goroutines have done
	defer c.queue.ShutDown()
	c.logger.Info("run: initiating")
	c.handler.Init()
	// Run the informer to list and watch resources
	go c.informer.Run(stopCh)
	go c.nodeInformer.Run(stopCh)
	go c.deplInformer.Run(stopCh)
	go c.daemonInformer.Run(stopCh)
	go c.stateInformer.Run(stopCh)

	// Synchronization to settle resources one
	if !cache.WaitForCacheSync(stopCh, c.informer.HasSynced, c.nodeInformer.HasSynced, c.deplInformer.HasSynced, c.daemonInformer.HasSynced, c.stateInformer.HasSynced) {
		utilruntime.HandleError(fmt.Errorf("Error syncing cache"))
		return
	}
	c.logger.Info("run: cache sync complete")
	// Operate the runWorker
	go wait.Until(c.runWorker, time.Second, stopCh)

	<-stopCh
}

// To process new objects added to the queue
func (c *controller) runWorker() {
	log.Info("runWorker: starting")
	// Run processNextItem for all the changes
	for c.processNextItem() {
		log.Info("runWorker: processing next item")
	}

	log.Info("runWorker: completed")
}

// This function deals with the queue and sends each item in it to the specified handler to be processed.
func (c *controller) processNextItem() bool {
	log.Info("processNextItem: start")
	// Fetch the next item of the queue
	event, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(event)
	// Get the key string
	keyRaw := event.(informerevent).key
	// Use the string key to get the object from the indexer
	item, exists, err := c.informer.GetIndexer().GetByKey(keyRaw)
	if err != nil {
		if c.queue.NumRequeues(event.(informerevent).key) < 5 {
			c.logger.Errorf("Controller.processNextItem: Failed processing item with key %s with error %v, retrying", event.(informerevent).key, err)
			c.queue.AddRateLimited(event.(informerevent).key)
		} else {
			c.logger.Errorf("Controller.processNextItem: Failed processing item with key %s with error %v, no more retries", event.(informerevent).key, err)
			c.queue.Forget(event.(informerevent).key)
			utilruntime.HandleError(err)
		}
	}

	if !exists {
		if event.(informerevent).function == delete {
			c.logger.Infof("Controller.processNextItem: object deleted detected: %s", keyRaw)
			c.handler.ObjectDeleted(item, event.(informerevent).delta)
		}
	} else {
		if event.(informerevent).function == create {
			c.logger.Infof("Controller.processNextItem: object created detected: %s", keyRaw)
			c.handler.ObjectCreated(item)
		} else if event.(informerevent).function == update {
			c.logger.Infof("Controller.processNextItem: object updated detected: %s", keyRaw)
			c.handler.ObjectUpdated(item, event.(informerevent).delta)
		}
	}
	c.queue.Forget(event.(informerevent).key)

	if c.queue.Len() == 0 {
		go c.handler.ConfigureControllers()
	}

	return true
}

// dry function remove the same values of the old and new objects from the old object to have
// the slice of deleted values.
func dry(oldSlice []selectivedeployment_v1.Controller, newSlice []selectivedeployment_v1.Controller) []string {
	var uniqueSlice []string
	for _, oldValue := range oldSlice {
		exists := false
		for _, newValue := range newSlice {
			if oldValue.Type == newValue.Type && oldValue.Name == newValue.Name {
				exists = true
			}
		}
		if !exists {
			uniqueSlice = append(uniqueSlice, fmt.Sprintf("%s?/delta/? %s", oldValue.Type, oldValue.Name))
		}
	}
	return uniqueSlice
}

func generateRandomString(n int) string {
	var letter = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ")

	b := make([]rune, n)
	for i := range b {
		b[i] = letter[rand.Intn(len(letter))]
	}
	return string(b)
}
