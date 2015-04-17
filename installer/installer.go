package installer

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"

	"github.com/cznic/ql"
	"github.com/flynn/flynn/Godeps/_workspace/src/github.com/awslabs/aws-sdk-go/aws"
	log "github.com/flynn/flynn/Godeps/_workspace/src/gopkg.in/inconshreveable/log15.v2"
)

var ClusterNotFoundError = errors.New("Cluster not found")

type Installer struct {
	db            *sql.DB
	events        []*Event
	subscriptions []*Subscription
	clusters      []interface{}
	logger        log.Logger

	dbMtx        sync.RWMutex
	eventsMtx    sync.Mutex
	subscribeMtx sync.Mutex
	clustersMtx  sync.RWMutex
}

func NewInstaller(l log.Logger) *Installer {
	installer := &Installer{
		events:        make([]*Event, 0),
		subscriptions: make([]*Subscription, 0),
		clusters:      make([]interface{}, 0),
		logger:        l,
	}
	if err := installer.openDB(); err != nil {
		panic(err)
	}
	return installer
}

func (i *Installer) LaunchCluster(c interface{}) error {
	switch v := c.(type) {
	case *AWSCluster:
		return i.launchAWSCluster(v)
	default:
		return fmt.Errorf("Invalid cluster type %T", c)
	}
}

func (i *Installer) launchAWSCluster(c *AWSCluster) error {
	if err := c.SetDefaultsAndValidate(); err != nil {
		return err
	}

	if err := i.saveAWSCluster(c); err != nil {
		return err
	}

	i.clustersMtx.Lock()
	i.clusters = append(i.clusters, c)
	i.clustersMtx.Unlock()
	i.SendEvent(&Event{
		Type:      "new_cluster",
		Cluster:   c.cluster,
		ClusterID: c.cluster.ID,
	})
	c.Run()
	return nil
}

func (i *Installer) saveAWSCluster(c *AWSCluster) error {
	i.dbMtx.Lock()
	defer i.dbMtx.Unlock()

	clusterFields, err := ql.Marshal(c.cluster)
	if err != nil {
		return err
	}
	awsFields, err := ql.Marshal(c)
	if err != nil {
		return err
	}
	clustersVStr := make([]string, 0, len(clusterFields))
	awsVStr := make([]string, 0, len(awsFields))
	fields := make([]interface{}, 0, len(clusterFields)+len(awsFields))
	for idx, f := range clusterFields {
		clustersVStr = append(clustersVStr, fmt.Sprintf("$%d", idx+1))
		fields = append(fields, f)
	}
	offset := len(clusterFields)
	for idx, f := range awsFields {
		awsVStr = append(awsVStr, fmt.Sprintf("$%d", idx+1+offset))
		fields = append(fields, f)
	}

	list, err := ql.Compile(fmt.Sprintf(`
		INSERT INTO clusters VALUES (%s);
		INSERT INTO aws_clusters VALUES(%s);
	`, strings.Join(clustersVStr, ", "), strings.Join(awsVStr, ", ")))
	if err != nil {
		return err
	}
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec(list.String(), fields...)
	if err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (i *Installer) SaveAWSCredentials(id, secret string) error {
	i.dbMtx.Lock()
	defer i.dbMtx.Unlock()
	tx, err := i.db.Begin()
	if err != nil {
		return err
	}
	_, err = tx.Exec(`
		INSERT INTO credentials (ID, Secret) VALUES ($1, $2);
  `, id, secret)
	if err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (i *Installer) FindAWSCredentials(id string) (aws.CredentialsProvider, error) {
	if id == "aws_env" {
		return aws.EnvCreds()
	}
	var secret string

	i.dbMtx.RLock()
	defer i.dbMtx.RUnlock()

	if err := i.db.QueryRow(`SELECT Secret FROM credentials WHERE id == $1 LIMIT 1`, id).Scan(&secret); err != nil {
		return nil, err
	}
	return aws.Creds(id, secret, ""), nil
}

func (i *Installer) FindCluster(id string) (*Cluster, error) {
	i.clustersMtx.RLock()
	for _, c := range i.clusters {
		if cluster, ok := c.(*AWSCluster); ok {
			if cluster.ClusterID == id {
				i.clustersMtx.RUnlock()
				return cluster.cluster, nil
			}
		}
	}
	i.clustersMtx.RUnlock()

	i.dbMtx.RLock()
	defer i.dbMtx.RUnlock()

	c := &Cluster{ID: id, installer: i}

	err := i.db.QueryRow(`
	SELECT CredentialID, Type, State, NumInstances, ControllerKey, ControllerPin, DashboardLoginToken, CACert, SSHKeyName, VpcCidr, SubnetCidr, DiscoveryToken, DNSZoneID FROM clusters WHERE ID == $1 LIMIT 1
  `, c.ID).Scan(&c.CredentialID, &c.Type, &c.State, &c.NumInstances, &c.ControllerKey, &c.ControllerPin, &c.DashboardLoginToken, &c.CACert, &c.SSHKeyName, &c.VpcCidr, &c.SubnetCidr, &c.DiscoveryToken, &c.DNSZoneID)
	if err != nil {
		return nil, err
	}

	domain := &Domain{ClusterID: c.ID}
	err = i.db.QueryRow(`
  SELECT Name, Token FROM domains WHERE ClusterID == $1 LIMIT 1
  `, c.ID).Scan(&domain.Name, &domain.Token)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		c.Domain = domain
	}
	return c, nil
}

func (i *Installer) DeleteCluster(id string) error {
	i.dbMtx.Lock()
	_, err := i.FindCluster(id)
	i.dbMtx.Unlock()
	if err != nil {
		return err
	}

	i.clustersMtx.Lock()
	clusters := make([]interface{}, 0, len(i.clusters))
	for _, c := range i.clusters {
		cID := reflect.Indirect(reflect.ValueOf(c)).FieldByName("ID").Interface().(string)
		if cID != id {
			clusters = append(clusters, c)
		}
	}
	i.clusters = clusters
	i.clustersMtx.Unlock()

	// TODO(jvatic): remove from database once stack deletion complete
	// TODO(jvatic): find AWS cluster and run Delete()
	i.SendEvent(&Event{ // TODO(jvatic): Send two events, one before cleanup and one after
		Type:      "cluster_deleted",
		ClusterID: id,
	})
	return nil
}
